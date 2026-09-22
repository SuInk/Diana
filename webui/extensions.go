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
	// 只有真的改了东西才记操作日志。原来按「不是 read 和 list」判断，presets、
	// preset_verify 这类纯查询也被记成「扩展管理操作已完成」，翻审计记录时分不出
	// 谁改过扩展。分类收在 agent.ExtensionOperationMutatesState 一处。
	if c.Request.Method == http.MethodPost && agent.ExtensionRequestMutatesState(req) {
		recordRequestOperation(c, h.logs, "extensions_"+req.Operation, "扩展管理操作已完成", req.Name, map[string]any{"kind": req.Kind, "profile_id": req.ProfileID})
	}
	if result == nil {
		result = gin.H{"ok": true}
	}
	c.JSON(http.StatusOK, result)
}
