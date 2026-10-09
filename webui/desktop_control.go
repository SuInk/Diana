// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package webui

import (
	"errors"
	"github.com/SuInk/diana/model/desktopctl"
	"github.com/gin-gonic/gin"
	"net/http"
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
	r.GET("/api/desktop-control/jobs", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"jobs": h.jobs.List("")})
	})
	r.POST("/api/desktop-control/jobs/:id/:action", h.jobAction)
}
func (h *DesktopControlHandler) status(c *gin.Context) {
	configured, detail := h.local.Status()
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"policy": h.registry.Policy(), "ready": h.hub.Ready(), "helper_configured": configured, "detail": detail, "connections": h.hub.Connections()})
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
	for _, v := range h.hub.Connections() {
		if conn, ok := h.hub.Connection(v.ID); ok {
			conn.SetTakeover(p.Active, "控制台人工接管")
		}
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
