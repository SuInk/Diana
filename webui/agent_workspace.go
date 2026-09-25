// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/gin-gonic/gin"
)

// 工作区是 Agent 读写文件、跑命令的目录，位置跟着数据库走、不做配置项。它藏在
// ~/Library/Application Support 或容器的数据卷里，主人想看机器人写了什么、截了什么
// 图，以前只能 SSH 上去翻。这里给一个只读的浏览入口：能列目录、能预览、能下载，
// 不能改——改文件是 Agent 的事，WebUI 这边多一条写入路径就多一处要防的地方。
//
// 请求里的路径按字面限定在工作区之内（不认「..」和绝对路径），符号链接照常跟过去，
// 指到外面也能看：能登录 WebUI 的只有管理员，Agent 链出去的东西本来就在他自己的
// 机器上，拦下来只会让人又回去 SSH。运行时的凭据配置（.mcp.json、扩展覆盖、编码代理
// 登录目录）同理照常给看，只在列表里打个标记，提醒这里面是明文令牌。
type AgentWorkspaceHandler struct {
	root func() string
}

// agentWorkspaceListLimit 是单个目录最多列出的条目数。编码代理克隆下来的仓库里
// node_modules 这类目录动辄上万项，全列出来页面会卡死，也没人会一条条翻。
const agentWorkspaceListLimit = 1000

func NewAgentWorkspaceHandler(root func() string) *AgentWorkspaceHandler {
	return &AgentWorkspaceHandler{root: root}
}

func (h *AgentWorkspaceHandler) Register(router gin.IRouter) {
	router.GET("/api/system/workspace", h.list)
	router.GET("/api/system/workspace/file", h.file)
}

type AgentWorkspaceEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"` // dir、file；目标已经不在了的链接是 link
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Symlink 表示这一项本身是符号链接，Kind 是它指向的东西。
	Symlink   bool `json:"symlink,omitempty"`
	Protected bool `json:"protected,omitempty"`
}

type AgentWorkspaceListing struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Exists 为 false 表示工作区还没建出来：Agent 第一次写文件时才会创建。
	Exists    bool                  `json:"exists"`
	Entries   []AgentWorkspaceEntry `json:"entries"`
	Truncated bool                  `json:"truncated,omitempty"`
}

func (h *AgentWorkspaceHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	root := h.root()
	rel, ok := workspaceRelPath(c.Query("path"))
	if !ok {
		writeError(c, http.StatusBadRequest, errors.New("路径必须在工作区之内"))
		return
	}
	listing, err := listAgentWorkspace(root, rel)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, listing)
}

func listAgentWorkspace(root, rel string) (AgentWorkspaceListing, error) {
	listing := AgentWorkspaceListing{Root: root, Path: rel, Entries: []AgentWorkspaceEntry{}}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) && rel == "." {
			listing.Path = ""
			return listing, nil
		}
		return listing, err
	}
	listing.Exists = true
	full := filepath.Join(root, rel)
	info, err := os.Stat(full)
	if err != nil {
		return listing, err
	}
	if !info.IsDir() {
		return listing, errWorkspaceNotDir
	}
	names, err := os.ReadDir(full)
	if err != nil {
		return listing, err
	}
	cfg := agent.Config{WorkDir: root}
	for _, item := range names {
		child := path.Join(filepath.ToSlash(rel), item.Name())
		entry := AgentWorkspaceEntry{Name: item.Name(), Path: child, Kind: "file"}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			entry.Symlink = true
			if target, err := os.Stat(filepath.Join(full, item.Name())); err == nil {
				info = target
			} else {
				entry.Kind = "link"
			}
		}
		if entry.Kind != "link" {
			if info.IsDir() {
				entry.Kind = "dir"
			} else if !info.Mode().IsRegular() {
				continue
			}
		}
		if entry.Kind == "file" {
			entry.Size = info.Size()
		}
		entry.Modified = info.ModTime()
		entry.Protected = agent.WorkspaceFileProtected(cfg, filepath.FromSlash(child))
		listing.Entries = append(listing.Entries, entry)
	}
	sort.Slice(listing.Entries, func(i, j int) bool {
		a, b := listing.Entries[i], listing.Entries[j]
		if (a.Kind == "dir") != (b.Kind == "dir") {
			return a.Kind == "dir"
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if len(listing.Entries) > agentWorkspaceListLimit {
		listing.Entries = listing.Entries[:agentWorkspaceListLimit]
		listing.Truncated = true
	}
	if rel == "." {
		listing.Path = ""
	} else {
		listing.Path = filepath.ToSlash(rel)
	}
	return listing, nil
}

// workspaceMediaTypes 是能在页面里直接打开的图片、音视频和 PDF，按扩展名认。SVG 也在
// 里面：页面用 <img> 显示它，脚本不会跑；有人直接打开这个地址时，下面的 sandbox CSP
// 同样不让它在控制台的源里执行。
var workspaceMediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp", ".avif": "image/avif", ".ico": "image/x-icon",
	".svg": "image/svg+xml",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac",
	".pdf": "application/pdf",
}

// workspaceSniffedMediaTypes 是没有扩展名（或扩展名不对）时，按内容认出来也可以直接
// 打开的类型。SVG 不在这里：内容嗅探不会给出 image/svg+xml。
var workspaceSniffedMediaTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/bmp": true,
	"video/mp4": true, "video/webm": true, "audio/mpeg": true, "audio/wave": true, "audio/ogg": true,
	"application/pdf": true,
}

func (h *AgentWorkspaceHandler) file(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	root := h.root()
	rel, ok := workspaceRelPath(c.Query("path"))
	if !ok || rel == "." {
		writeError(c, http.StatusBadRequest, errors.New("路径必须是工作区里的文件"))
		return
	}
	file, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(c, http.StatusBadRequest, errors.New("只能打开普通文件"))
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeWorkspaceError(c, err)
		return
	}
	contentType, inline := workspaceContentType(info.Name(), head[:n])
	disposition := "attachment"
	if inline && c.Query("download") != "1" {
		disposition = "inline"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Name()}))
	// 文件是 Agent 写的，内容不可信：禁止嗅探，也不让它在控制台的源里跑脚本。PDF 例外：
	// 带 sandbox 的响应浏览器不肯用内置阅读器打开，而阅读器里的脚本本来就和页面隔离。
	c.Header("X-Content-Type-Options", "nosniff")
	if contentType != "application/pdf" {
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

// workspaceContentType 决定文件以什么类型、能不能在页面里直接打开：先按扩展名认
// 常用的媒体格式，再按内容嗅探；是文本的一律按纯文本给（HTML 也只给源码，不渲染），
// 其余按二进制下载。
func workspaceContentType(name string, head []byte) (string, bool) {
	if media, ok := workspaceMediaTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return media, true
	}
	detected := http.DetectContentType(head)
	base := strings.TrimSpace(strings.SplitN(detected, ";", 2)[0])
	if workspaceSniffedMediaTypes[base] {
		return base, true
	}
	if strings.HasPrefix(base, "text/") || workspaceTextExtension(name) {
		return "text/plain; charset=utf-8", true
	}
	return "application/octet-stream", false
}

// workspaceTextExtension 兜住 DetectContentType 认不出来的文本：空文件、开头是
// 花括号的 JSON 会被当成 text/plain，但带 BOM 或较短的 YAML、脚本有时落到二进制。
func workspaceTextExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".markdown", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".env",
		".csv", ".tsv", ".log", ".srt", ".vtt", ".lrc", ".html", ".htm", ".css", ".xml", ".sql",
		".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".vue", ".sh", ".bash", ".zsh", ".ps1",
		".c", ".h", ".cpp", ".java", ".kt", ".rs", ".rb", ".php", ".swift", ".lua":
		return true
	}
	return false
}

var errWorkspaceNotDir = errors.New("不是目录")

// workspaceRelPath 把请求里的路径收成工作区内的相对路径；空串是工作区根目录。
func workspaceRelPath(raw string) (string, bool) {
	raw = strings.Trim(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ".", true
	}
	rel := filepath.FromSlash(path.Clean(raw))
	if !filepath.IsLocal(rel) {
		return "", false
	}
	return rel, true
}

func writeWorkspaceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(c, http.StatusNotFound, errors.New("找不到这个路径"))
	case errors.Is(err, errWorkspaceNotDir):
		writeError(c, http.StatusBadRequest, err)
	default:
		writeError(c, http.StatusInternalServerError, err)
	}
}
