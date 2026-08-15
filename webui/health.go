package webui

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler 提供轻量健康检查接口，供 Dashboard 和探活使用。
type HealthHandler struct {
	startedAt time.Time
	version   string
}

// NewHealthHandler 创建 HealthHandler；version 从构建信息里读取 VCS revision。
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{
		startedAt: time.Now(),
		version:   buildVersion(),
	}
}

// Register 注册当前模块的路由或能力。
func (h *HealthHandler) Register(router gin.IRouter) {
	router.GET("/api/health", h.health)
}

// health 返回进程健康状态、启动时间和版本。
func (h *HealthHandler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"started_at":     h.startedAt,
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"version":        h.version,
	})
}

// buildVersion 从 Go 构建信息提取 VCS 版本号，源码运行时返回 dev。
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	revision := ""
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision
}
