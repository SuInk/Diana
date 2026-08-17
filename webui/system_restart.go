package webui

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// RestartHandler 暴露服务自重启接口。实际的进程替换由 cmd/webui 注入的
// trigger 完成：优雅关停 HTTP server 后原地重启同一个二进制。
type RestartHandler struct {
	trigger func()
	logs    AppLogWriter
}

// NewRestartHandler 创建 RestartHandler 实例。
func NewRestartHandler(trigger func()) *RestartHandler {
	return &RestartHandler{trigger: trigger}
}

// SetLogStore 注入操作日志写入器。
func (h *RestartHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册重启路由。
func (h *RestartHandler) Register(router gin.IRouter) {
	router.POST("/api/system/restart", h.restart)
}

func (h *RestartHandler) restart(c *gin.Context) {
	var payload struct {
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if payload.Confirmation != "restart-service" {
		writeError(c, http.StatusBadRequest, errors.New("重启服务需要明确确认"))
		return
	}
	recordRequestOperation(c, h.logs, "system.restart", "服务重启已触发", "", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
	// 延迟触发，先让本次响应完整送达客户端。
	go func() {
		time.Sleep(500 * time.Millisecond)
		h.trigger()
	}()
}
