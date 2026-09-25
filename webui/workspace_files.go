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
	"path"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

// 工作目录页：以前 Agent 工作目录里有什么，只能 SSH 上去看。长期区存了哪些、下载目录
// 攒了多少、回收站里是什么，管理员在界面上看得到、下载得了、删得掉。
//
// 这些接口挂在 /api 下，跟其余管理接口一样过 WebUI 登录鉴权。路径校验不另写一套：
// 下载和删除都走 agent 包里文件工具用的那套（safePath + 凭据名单 + os.OpenRoot）。

// workspaceFilesRuntime 是工作目录页要用到的运行时能力：按机器人 ID 认出长期区归谁，
// 判断编码工作区还有没有人用。
type workspaceFilesRuntime interface {
	ProfileConfigs() []assistant.BotConfig
	CodingWorkspaceReferenced() func(string) bool
}

type WorkspaceFilesHandler struct {
	root    func() string
	runtime workspaceFilesRuntime
	logs    AppLogWriter
	now     func() time.Time
}

// NewWorkspaceFilesHandler 创建工作目录页的接口；runtime 可以为 nil（只是认不出机器人名字）。
func NewWorkspaceFilesHandler(runtime workspaceFilesRuntime) *WorkspaceFilesHandler {
	return &WorkspaceFilesHandler{root: assistant.AgentWorkspaceDir, runtime: runtime, now: time.Now}
}

// SetLogStore 注入操作日志写入器：删除和清空回收站都记一笔。
func (h *WorkspaceFilesHandler) SetLogStore(store AppLogWriter) { h.logs = store }

func (h *WorkspaceFilesHandler) Register(router gin.IRouter) {
	router.GET("/api/workspace/files", h.list)
	router.GET("/api/workspace/download", h.download)
	router.POST("/api/workspace/delete", h.delete)
	router.POST("/api/workspace/trash/empty", h.emptyTrash)
}

func (h *WorkspaceFilesHandler) config() agent.Config {
	return agent.Config{WorkDir: h.root()}
}

func (h *WorkspaceFilesHandler) list(c *gin.Context) {
	opts := agent.WorkspaceCleanupOptions{Now: h.now()}
	names := map[string]string{}
	if h.runtime != nil {
		opts.CodingReferenced = h.runtime.CodingWorkspaceReferenced()
		for _, profile := range h.runtime.ProfileConfigs() {
			if dir := agent.KeepBotDir(profile.ID); dir != "" {
				names[dir] = firstNonEmptyString(profile.Name, profile.ID)
			}
		}
	}
	listing, err := agent.ListWorkspace(h.root(), opts, agent.DefaultWorkspaceListLimit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	for i := range listing.Areas {
		if listing.Areas[i].Key == "keep" {
			listing.Areas[i].BotName = names[listing.Areas[i].BotID]
		}
	}
	c.JSON(http.StatusOK, listing)
}

func (h *WorkspaceFilesHandler) download(c *gin.Context) {
	rel := strings.TrimSpace(c.Query("path"))
	if rel == "" {
		writeError(c, http.StatusBadRequest, errors.New("缺少 path"))
		return
	}
	file, info, clean, err := agent.OpenWorkspaceFile(h.config(), rel)
	if err != nil {
		writeError(c, workspaceErrorStatus(err), err)
		return
	}
	defer file.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(clean)})
	if disposition == "" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", disposition)
	// 下载的是任意用户文件：不让浏览器按内容猜类型当页面渲染。
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", "application/octet-stream")
	http.ServeContent(c.Writer, c.Request, path.Base(clean), info.ModTime(), io.ReadSeeker(file))
}

type workspaceDeleteRequest struct {
	Path string `json:"path"`
}

func (h *WorkspaceFilesHandler) delete(c *gin.Context) {
	var request workspaceDeleteRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Path) == "" {
		writeError(c, http.StatusBadRequest, errors.New("缺少 path"))
		return
	}
	trashPath, err := agent.TrashWorkspacePath(h.config(), request.Path, h.now())
	if err != nil {
		writeError(c, workspaceErrorStatus(err), err)
		return
	}
	recordAppLog(c.Request.Context(), h.logs, storage.AppLogEntry{
		Kind:    storage.LogKindOperation,
		Level:   storage.LogLevelInfo,
		Action:  "workspace_file_delete",
		Message: "工作目录文件移到回收站：" + request.Path,
		Actor:   requestActor(c),
		Target:  request.Path,
		Metadata: map[string]any{
			"trash_path": trashPath,
		},
	})
	c.JSON(http.StatusOK, gin.H{"trash_path": trashPath})
}

func (h *WorkspaceFilesHandler) emptyTrash(c *gin.Context) {
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

// workspaceErrorStatus 把路径校验的失败归成 4xx：越界、凭据、目录、找不到都是请求
// 本身的问题，照原话回给管理员。
func workspaceErrorStatus(err error) int {
	if errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), "工作目录里没有") {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
