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
// 所有访问都经 os.Root：符号链接指到工作区外面时打不开，「..」也走不出去。运行时
// 自己的凭据配置（.mcp.json、扩展覆盖、编码代理登录目录）和文件工具同一份名单，
// 列目录时照常出现但内容不给。
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
	Kind     string    `json:"kind"` // dir、file；指到工作区外面或已失效的链接是 link
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
	dir, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && rel == "." {
			listing.Path = ""
			return listing, nil
		}
		return listing, err
	}
	defer dir.Close()
	listing.Exists = true
	info, err := dir.Stat(rel)
	if err != nil {
		return listing, err
	}
	if !info.IsDir() {
		return listing, errWorkspaceNotDir
	}
	if agent.WorkspaceFileProtected(agent.Config{WorkDir: root}, rel) {
		return listing, errWorkspaceProtected
	}
	handle, err := dir.Open(rel)
	if err != nil {
		return listing, err
	}
	names, err := handle.ReadDir(-1)
	_ = handle.Close()
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
			// 经 os.Root 解析：指到工作区外面的链接在这里就会失败，页面上不给点开。
			if target, err := dir.Stat(filepath.FromSlash(child)); err == nil {
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

// inlineWorkspaceImageTypes 是可以直接在页面里显示的图片。SVG 不在里面：它能带
// 脚本，而这个接口和控制台同源。
var inlineWorkspaceImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
}

func (h *AgentWorkspaceHandler) file(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	root := h.root()
	rel, ok := workspaceRelPath(c.Query("path"))
	if !ok || rel == "." {
		writeError(c, http.StatusBadRequest, errors.New("路径必须是工作区里的文件"))
		return
	}
	if agent.WorkspaceFileProtected(agent.Config{WorkDir: root}, rel) {
		writeWorkspaceError(c, errWorkspaceProtected)
		return
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	defer dir.Close()
	file, err := dir.Open(rel)
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
	// 文件是 Agent 写的，内容不可信：禁止嗅探，也不让它在控制台的源里跑脚本。
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

// workspaceContentType 决定文件以什么类型、能不能在页面里直接打开。只放行位图和
// 纯文本；HTML、SVG 之类一律按纯文本给源码，其余按二进制下载。
func workspaceContentType(name string, head []byte) (string, bool) {
	detected := http.DetectContentType(head)
	base := strings.TrimSpace(strings.SplitN(detected, ";", 2)[0])
	if inlineWorkspaceImageTypes[base] {
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
	case ".txt", ".md", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini", ".csv", ".tsv", ".log",
		".go", ".py", ".js", ".mjs", ".ts", ".vue", ".sh", ".html", ".htm", ".css", ".xml", ".svg", ".sql":
		return true
	}
	return false
}

var (
	errWorkspaceNotDir    = errors.New("不是目录")
	errWorkspaceProtected = errors.New("这是运行时的凭据配置，不在 WebUI 里显示")
)

// workspaceRelPath 把请求里的路径收成 os.Root 认的相对路径；空串是工作区根目录。
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
	case errors.Is(err, errWorkspaceProtected):
		writeError(c, http.StatusForbidden, err)
	case errors.Is(err, errWorkspaceNotDir):
		writeError(c, http.StatusBadRequest, err)
	default:
		// os.Root 拒绝越界时报的是「path escapes from parent」，对用户就是走不出去。
		if strings.Contains(err.Error(), "escapes") {
			writeError(c, http.StatusBadRequest, errors.New("这个链接指到了工作区外面"))
			return
		}
		writeError(c, http.StatusInternalServerError, err)
	}
}
