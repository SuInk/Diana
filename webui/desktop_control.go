// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package webui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/SuInk/diana/model/desktopctl"
	"github.com/gin-gonic/gin"
)

type DesktopControlHandler struct {
	registry *desktopctl.Registry
	hub      *desktopctl.Hub
	local    *desktopctl.LocalService
	jobs     *desktopctl.JobManager
}

func NewDesktopControlHandler(r *desktopctl.Registry, h *desktopctl.Hub, l *desktopctl.LocalService, j *desktopctl.JobManager) *DesktopControlHandler {
	return &DesktopControlHandler{r, h, l, j}
}

// All endpoints require the existing WebUI /api session authentication.
func (h *DesktopControlHandler) Register(r gin.IRouter) {
	r.GET("/api/desktop-control/status", h.status)
	r.PUT("/api/desktop-control/policy", h.policy)
	r.POST("/api/desktop-control/takeover", h.takeover)
	r.POST("/api/desktop-control/permissions/request", h.requestPermissions)
	r.POST("/api/desktop-control/reconnect", h.reconnect)
	r.GET("/api/desktop-control/jobs", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"jobs": h.jobs.List("")})
	})
	r.POST("/api/desktop-control/jobs/:id/:action", h.jobAction)
}
func (h *DesktopControlHandler) status(c *gin.Context) {
	configured, detail := h.local.Status()
	var permissions *desktopctl.Permissions
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if p := h.local.Process(); p != nil {
		if v, err := p.Permissions(ctx); err == nil {
			permissions = &v
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"permissions": permissions, "authorize": desktopAuthorizeTarget(), "takeover": h.registry.EmergencyStop(), "policy": h.registry.Policy(), "ready": h.hub.Ready(), "helper_configured": configured, "executor": h.local.Executor(), "detail": detail, "connections": h.hub.Connections()})
}
func (h *DesktopControlHandler) policy(c *gin.Context) {
	var p desktopctl.Policy
	if c.ShouldBindJSON(&p) != nil {
		writeError(c, 400, errors.New("桌面策略格式错误"))
		return
	}
	saved, err := h.registry.SetPolicy(c.Request.Context(), p)
	if err != nil {
		writeError(c, 500, err)
		return
	}
	// Cancel all in-flight local work when permissions change, then reconnect.
	h.local.Close()
	h.hub.CloseAll()
	h.local.Sync(c.Request.Context())
	c.JSON(200, gin.H{"policy": saved})
}
func (h *DesktopControlHandler) takeover(c *gin.Context) {
	var p struct {
		Active bool `json:"active"`
	}
	if c.ShouldBindJSON(&p) != nil {
		writeError(c, 400, errors.New("接管状态格式错误"))
		return
	}
	if err := h.hub.SetTakeover(c.Request.Context(), p.Active, "控制台人工接管"); err != nil {
		writeError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"active": p.Active})
}
func (h *DesktopControlHandler) jobAction(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	var j desktopctl.Job
	var err error
	switch c.Param("action") {
	case "pause":
		j, err = h.jobs.Pause(ctx, id, "控制台暂停")
	case "resume":
		j, err = h.jobs.Resume(ctx, id)
	case "confirm":
		j, err = h.jobs.Confirm(ctx, id)
	case "cancel":
		j, err = h.jobs.Cancel(ctx, id, "控制台取消")
	default:
		writeError(c, http.StatusBadRequest, errors.New("未知任务操作"))
		return
	}
	if err != nil {
		writeError(c, http.StatusConflict, err)
		return
	}
	c.JSON(200, gin.H{"job": j})
}

// desktopAuthorizeTarget 是要在「隐私与安全性」里打开的那一项。macOS 把屏幕录制
// 和辅助功能算在运行 Diana 的程序名下（cua-driver 和 helper 都是它的子进程）：一键
// 安装跑在 Diana.app 里，系统设置列表显示的就是「Diana」；手动部署跑裸程序，列表
// 显示程序文件名，这时要给出路径，主人才找得到。
type desktopAuthorize struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Bundle bool   `json:"bundle"`
}

func desktopAuthorizeTarget() *desktopAuthorize {
	if runtime.GOOS != "darwin" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return authorizeTargetFor(exe)
}

func authorizeTargetFor(exe string) *desktopAuthorize {
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		bundle := exe[:i+len(".app")]
		return &desktopAuthorize{Name: strings.TrimSuffix(filepath.Base(bundle), ".app"), Path: bundle, Bundle: true}
	}
	return &desktopAuthorize{Name: filepath.Base(exe), Path: exe}
}

// desktopSettingsPanes 是系统设置里两个隐私面板的地址，只在本机打开，不接受客户端传入。
var desktopSettingsPanes = map[string]string{
	"screen_recording": "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"accessibility":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
}

// openDesktopSettingsPane 在运行 Diana 的 Mac 上打开系统设置，测试里换掉。
var openDesktopSettingsPane = func(url string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("只有 macOS 有这个设置")
	}
	return exec.Command("open", url).Run()
}

// requestPermissions 让执行器触发 macOS 授权弹窗（这一步会把 Diana 加进系统设置的
// 列表），再在这台 Mac 上打开对应的隐私面板，主人只需打开开关。
func (h *DesktopControlHandler) requestPermissions(c *gin.Context) {
	var body struct {
		Kind string `json:"kind"`
	}
	_ = c.ShouldBindJSON(&body)
	pane, ok := desktopSettingsPanes[body.Kind]
	if !ok {
		writeError(c, 400, errors.New("kind 只能是 screen_recording 或 accessibility"))
		return
	}
	p := h.local.Process()
	if p == nil {
		writeError(c, 409, errors.New("先打开桌面控制并等执行器就绪"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	permissions, err := p.RequestPermissions(ctx)
	if err != nil {
		writeError(c, 502, err)
		return
	}
	opened := openDesktopSettingsPane(pane) == nil
	c.JSON(200, gin.H{"permissions": permissions, "opened": opened, "authorize": desktopAuthorizeTarget()})
}

// reconnect 重启执行器进程：在系统设置里刚授权的权限，往往要新进程才认。
func (h *DesktopControlHandler) reconnect(c *gin.Context) {
	h.hub.CloseAll()
	h.local.Restart(c.Request.Context())
	c.JSON(200, gin.H{"ready": h.hub.Ready(), "executor": h.local.Executor()})
}
