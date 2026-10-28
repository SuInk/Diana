// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/runtimeconfig"
	"github.com/gin-gonic/gin"
)

// RuntimeConfigHandler 暴露启动参数、环境变量和 config.yaml 基础设施段的查看与写回。
// 写回只改文件，生效要重启，接口返回的快照用 pending 标出这些项。
type RuntimeConfigHandler struct {
	manager *runtimeconfig.Manager
	logs    AppLogWriter
}

// NewRuntimeConfigHandler 创建 RuntimeConfigHandler。
func NewRuntimeConfigHandler(manager *runtimeconfig.Manager) *RuntimeConfigHandler {
	return &RuntimeConfigHandler{manager: manager}
}

// SetLogStore 注入操作日志写入器。
func (h *RuntimeConfigHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册路由。
func (h *RuntimeConfigHandler) Register(router gin.IRouter) {
	router.GET("/api/system/runtime-config", h.get)
	router.PUT("/api/system/runtime-config", h.update)
	router.GET("/api/system/network", h.network)
}

// network 返回本机地址和端口。?public=1 才探测公网出口地址（会访问外部服务），
// ?force=1 跳过五分钟缓存。
func (h *RuntimeConfigHandler) network(c *gin.Context) {
	snap, err := h.manager.Network(c.Request.Context(), c.Query("public") == "1", c.Query("force") == "1")
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (h *RuntimeConfigHandler) get(c *gin.Context) {
	snap, err := h.manager.Snapshot()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (h *RuntimeConfigHandler) update(c *gin.Context) {
	var payload struct {
		Values map[string]string `json:"values"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	snap, err := h.manager.Update(payload.Values, runtimeconfig.ActorAdmin)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, runtimeconfig.ErrNotEditable) {
			status = http.StatusForbidden
		}
		writeError(c, status, err)
		return
	}
	// 日志只记改了哪些键，不记值：日志路径、代理地址这类值没必要进操作日志。
	keys := make([]string, 0, len(payload.Values))
	for key := range payload.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	recordRequestOperation(c, h.logs, "runtime_config_update", "运行配置已写入 config.yaml，重启后生效", strings.Join(keys, ","), nil)
	c.JSON(http.StatusOK, snap)
}
