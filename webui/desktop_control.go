// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package webui

import (
	"context"
	"errors"
	"github.com/SuInk/diana/model/desktopctl"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
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
	r.POST("/api/desktop-control/actions/:id/confirm", h.confirmAction)
	r.PUT("/api/desktop-control/policy", h.policy)
	r.POST("/api/desktop-control/takeover", h.takeover)
	r.GET("/api/desktop-control/jobs", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"jobs": h.jobs.List("")})
	})
	r.POST("/api/desktop-control/jobs/:id/:action", h.jobAction)
}
func (h *DesktopControlHandler) status(c *gin.Context) {
	configured, detail := h.local.Status()
	var permissions *desktopctl.Permissions
	var pending []desktopctl.ActionApproval
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if p := h.local.Process(); p != nil {
		if v, err := p.Permissions(ctx); err == nil {
			permissions = &v
		}
		pending, _ = p.PendingActions(ctx)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"permissions": permissions, "pending_actions": pending, "takeover": h.registry.EmergencyStop(), "policy": h.registry.Policy(), "ready": h.hub.Ready(), "helper_configured": configured, "detail": detail, "connections": h.hub.Connections()})
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

func (h *DesktopControlHandler) confirmAction(c *gin.Context) {
	if h.registry.EmergencyStop() || !h.registry.Policy().Enabled || !h.registry.Policy().WriteEnabled {
		writeError(c, 409, errors.New("请先结束接管并启用写操作"))
		return
	}
	p := h.local.Process()
	if p == nil {
		writeError(c, 409, errors.New("本机执行器不可用"))
		return
	}
	if err := p.ConfirmAction(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, 409, err)
		return
	}
	c.JSON(200, gin.H{"confirmed": true})
}
