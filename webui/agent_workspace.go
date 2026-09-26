// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"fmt"
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
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

// 文件页：工作区是 Agent 读写文件、跑命令的目录，位置跟着数据库走、不做配置项。它藏在
// ~/Library/Application Support 或容器的数据卷里，主人想看机器人写了什么、截了什么图、
// 长期区存了哪些、回收站里是什么，以前只能 SSH 上去翻。
//
// 这一页以前是两处：「文件」只能逐层浏览和预览，设置里的「工作目录」按分区列文件、能
// 下载和删除。现在合成一个处理器。
//
// 能登录 WebUI 的只有管理员，这台机器本来就是他的，所以这里什么都给看：
//
//   - 请求里的路径按字面限定在工作区之内（不认「..」和绝对路径），符号链接照常跟过去，
//     指到外面也能看，只在列表里标出来。拦下来只会让人又回去 SSH；
//   - 运行时配置和凭据（.mcp.json、.diana/、编码代理登录目录）照常列出、能打开，
//     列表里打标记，提醒里面是明文令牌。Agent 那层的凭据名单是防模型把令牌打进聊天的，
//     不套到管理员身上；
//   - 删除一律挪进回收站。凭据和运行时文件也能删，页面上多确认一次。
//
// 真正要守的是控制台自己的源：文件是 Agent 写的、内容不可信，响应一律 nosniff 加
// sandbox CSP，HTML 只给源码不渲染（见 file）。
//
// 接口都挂在 /api 下，跟其余管理接口一样过 WebUI 登录鉴权。
type AgentWorkspaceHandler struct {
	root    func() string
	runtime agentWorkspaceRuntime
	logs    AppLogWriter
	now     func() time.Time
}

// agentWorkspaceRuntime 是文件页要用到的运行时能力：按机器人 ID 认出长期区归谁，
// 判断编码工作区还有没有人用。
type agentWorkspaceRuntime interface {
	ProfileConfigs() []assistant.BotConfig
	CodingWorkspaceReferenced() func(string) bool
}

// agentWorkspaceListLimit 是单个目录最多列出的条目数。编码代理克隆下来的仓库里
// node_modules 这类目录动辄上万项，全列出来页面会卡死，也没人会一条条翻。
const agentWorkspaceListLimit = 1000

// NewAgentWorkspaceHandler 创建文件页的接口；runtime 可以为 nil（只是认不出机器人名字、
// 不报闲置的编码工作区）。
func NewAgentWorkspaceHandler(root func() string, runtime agentWorkspaceRuntime) *AgentWorkspaceHandler {
	return &AgentWorkspaceHandler{root: root, runtime: runtime, now: time.Now}
}

// SetLogStore 注入操作日志写入器：删除和清空回收站都记一笔。
func (h *AgentWorkspaceHandler) SetLogStore(store AppLogWriter) { h.logs = store }

func (h *AgentWorkspaceHandler) Register(router gin.IRouter) {
	router.GET("/api/system/workspace", h.list)
	router.GET("/api/system/workspace/file", h.file)
	router.GET("/api/workspace/files", h.overview)
	router.POST("/api/workspace/delete", h.delete)
	router.POST("/api/workspace/trash/empty", h.emptyTrash)
}

func (h *AgentWorkspaceHandler) config() agent.Config {
	return agent.Config{WorkDir: h.root()}
}

// botNames 把 keep/ 下的目录名对回机器人名字。
func (h *AgentWorkspaceHandler) botNames() map[string]string {
	names := map[string]string{}
	if h.runtime == nil {
		return names
	}
	for _, profile := range h.runtime.ProfileConfigs() {
		if dir := agent.KeepBotDir(profile.ID); dir != "" {
			names[dir] = firstNonEmptyString(profile.Name, profile.ID)
		}
	}
	return names
}

type AgentWorkspaceEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"` // dir、file；打不开的链接是 link
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Symlink 表示这一项本身是符号链接，Kind 是它指向的东西；External 表示指到了
	// 工作区外面。目标已经不在了的链接 Kind 是 link。
	Symlink  bool `json:"symlink,omitempty"`
	External bool `json:"external,omitempty"`
	// Protected 表示是运行时自己的配置或凭据（明文令牌、扩展开关、长期区索引）：
	// 照常能看，页面上打个标记。
	Protected bool `json:"protected,omitempty"`
	// 长期区的条目带上索引里记的说明：文件名说明不了「这是上周群里那张活动海报」。
	Description string    `json:"description,omitempty"`
	SavedBy     string    `json:"saved_by,omitempty"`
	SavedAt     time.Time `json:"saved_at,omitzero"`
}

type AgentWorkspaceListing struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Exists 为 false 表示工作区还没建出来：Agent 第一次写文件时才会创建。
	Exists    bool                  `json:"exists"`
	Entries   []AgentWorkspaceEntry `json:"entries"`
	Truncated bool                  `json:"truncated,omitempty"`
	// Area 是当前目录所在的分区和它的清理规则；根目录和编码仓库这类不按分区管的为空。
	Area *agent.WorkspaceAreaHint `json:"area,omitempty"`
}

func (h *AgentWorkspaceHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	rel, ok := workspaceRelPath(c.Query("path"))
	if !ok {
		writeError(c, http.StatusBadRequest, errors.New("路径必须在工作区之内"))
		return
	}
	listing, err := listAgentWorkspace(h.root(), rel)
	if err != nil {
		writeWorkspaceError(c, err)
		return
	}
	if listing.Area != nil && listing.Area.BotID != "" {
		listing.Area.BotName = h.botNames()[listing.Area.BotID]
	}
	c.JSON(http.StatusOK, listing)
}

func listAgentWorkspace(root, rel string) (AgentWorkspaceListing, error) {
	listing := AgentWorkspaceListing{Root: root, Path: filepath.ToSlash(rel), Entries: []AgentWorkspaceEntry{}}
	if rel == "." {
		listing.Path = ""
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) && rel == "." {
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
	items, err := os.ReadDir(full)
	if err != nil {
		return listing, err
	}
	protected := agent.WorkspaceProtectedFunc(agent.Config{WorkDir: root})
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	keep := agent.KeepEntriesIn(root, rel)
	for _, item := range items {
		child := path.Join(filepath.ToSlash(rel), item.Name())
		entry := AgentWorkspaceEntry{Name: item.Name(), Path: child, Kind: "file", Protected: protected(child)}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			entry.Symlink = true
			target := filepath.Join(full, item.Name())
			if resolved, err := os.Stat(target); err == nil {
				info = resolved
				entry.External = workspaceLinkEscapes(resolvedRoot, target)
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
		if record, ok := keep[child]; ok {
			entry.Description = record.Description
			entry.SavedBy = record.SavedBy
			entry.SavedAt = record.SavedAt
		}
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
	listing.Area = agent.WorkspaceAreaOf(filepath.ToSlash(rel))
	return listing, nil
}

// workspaceLinkEscapes 判断链接解析之后是不是落在工作区外面，只用来在列表里打标记。
func workspaceLinkEscapes(resolvedRoot, link string) bool {
	if resolvedRoot == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false
	}
	inside, err := filepath.Rel(resolvedRoot, resolved)
	return err != nil || !filepath.IsLocal(inside)
}

// overview 是页面顶部的分区卡片：各区的合计和清理规则、长期区配额，外加根下散落的
// 文件和闲置的编码工作区（只报告，不自动删）。
func (h *AgentWorkspaceHandler) overview(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	opts := agent.WorkspaceCleanupOptions{Now: h.now()}
	if h.runtime != nil {
		opts.CodingReferenced = h.runtime.CodingWorkspaceReferenced()
	}
	overview, err := agent.SummarizeWorkspace(h.root(), opts)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	names := h.botNames()
	for i := range overview.Areas {
		if overview.Areas[i].Key == "keep" {
			overview.Areas[i].BotName = names[overview.Areas[i].BotID]
		}
	}
	c.JSON(http.StatusOK, overview)
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

// file 给预览和下载用：download=1 时一律当附件。
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

type workspaceDeleteRequest struct {
	Path string `json:"path"`
}

// delete 把文件或目录挪进回收站，长期区索引跟着清掉。凭据和运行时文件也照删（页面上
// 多确认一次）；回收站里的东西不单条删，只能「清空回收站」。
func (h *AgentWorkspaceHandler) delete(c *gin.Context) {
	var request workspaceDeleteRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Path) == "" {
		writeError(c, http.StatusBadRequest, errors.New("缺少 path"))
		return
	}
	rel, ok := workspaceRelPath(request.Path)
	if !ok || rel == "." {
		writeError(c, http.StatusBadRequest, errors.New("路径必须是工作区里的文件或目录"))
		return
	}
	target := filepath.ToSlash(rel)
	trashPath, err := agent.TrashWorkspacePath(h.config(), target, h.now())
	if err != nil {
		// 回收站内部、长期区根目录、中途经链接出了工作区都是请求本身的问题，照原话回给管理员。
		status := http.StatusBadRequest
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(c, status, err)
		return
	}
	recordAppLog(c.Request.Context(), h.logs, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   "workspace_file_delete",
		Message:  "工作目录文件移到回收站：" + target,
		Actor:    requestActor(c),
		Target:   target,
		Metadata: map[string]any{"trash_path": trashPath},
	})
	c.JSON(http.StatusOK, gin.H{"trash_path": trashPath})
}

func (h *AgentWorkspaceHandler) emptyTrash(c *gin.Context) {
	files, bytes, err := agent.EmptyWorkspaceTrash(h.root())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	recordAppLog(c.Request.Context(), h.logs, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   "workspace_trash_empty",
		Message:  fmt.Sprintf("清空工作目录回收站：永久删除 %d 个文件", files),
		Actor:    requestActor(c),
		Target:   agent.WorkspaceTrashDir,
		Metadata: map[string]any{"deleted_files": files, "deleted_bytes": bytes},
	})
	c.JSON(http.StatusOK, gin.H{"deleted_files": files, "deleted_bytes": bytes})
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
