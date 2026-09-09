package webui

import (
	"context"
	"github.com/SuInk/diana/model/agent"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

type extensionAdminRuntime interface {
	AdministerExtensions(context.Context, agent.ExtensionAdminRequest) (any, error)
}

func (h *BotHandler) extensions(c *gin.Context) {
	r, ok := h.runtime.(extensionAdminRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "扩展管理不可用"})
		return
	}
	req := agent.ExtensionAdminRequest{Operation: "list", ProfileID: c.Query("profile")}
	if c.Request.Method == http.MethodPost {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 3<<20)
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "扩展请求格式错误或过大"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	result, err := r.AdministerExtensions(ctx, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if c.Request.Method == http.MethodPost && req.Operation != "read" && req.Operation != "list" {
		recordRequestOperation(c, h.logs, "assistant.extensions."+req.Operation, "扩展管理操作已完成", req.Name, map[string]any{"kind": req.Kind, "profile_id": req.ProfileID})
	}
	if result == nil {
		result = gin.H{"ok": true}
	}
	c.JSON(http.StatusOK, result)
}
