package webui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 控制台群管理接口：走全局登录鉴权，登录用户即管理员，
// 无需再经过 QQ 验证码流程（该流程保留给群管自助场景）。

type consoleGroupsResponse struct {
	Groups  []assistant.GroupConfig `json:"groups"`
	Plugins []assistant.PluginState `json:"plugins"`
}

type consoleGroupSavePayload struct {
	Config assistant.GroupConfig `json:"config"`
}

// registerConsoleGroupRoutes 注册控制台群配置直连路由。
func (h *QQBotHandler) registerConsoleGroupRoutes(router gin.IRouter) {
	router.GET("/api/qqbot/groups", h.listConsoleGroups)
	router.POST("/api/qqbot/groups", h.saveConsoleGroup)
}

// listConsoleGroups 返回全部群配置与插件清单。
func (h *QQBotHandler) listConsoleGroups(c *gin.Context) {
	base := h.runtime.Config()
	set := h.groupConfigs.Groups()
	groups := make([]assistant.GroupConfig, 0, len(set.Groups))
	for _, cfg := range set.Groups {
		groups = append(groups, cfg.WithDefaults(cfg.GroupID, base))
	}
	c.JSON(http.StatusOK, consoleGroupsResponse{
		Groups:  groups,
		Plugins: assistant.RedactStates(h.runtime.Plugins().List()),
	})
}

// saveConsoleGroup 创建或更新单个群配置。
func (h *QQBotHandler) saveConsoleGroup(c *gin.Context) {
	var payload consoleGroupSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.Config.GroupID)
	if _, err := strconv.ParseInt(groupID, 10, 64); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", fmt.Errorf("群号格式不正确"), groupID, nil)
		return
	}
	cfg := sanitizeGroupConfigPayload(payload.Config, groupID)
	saved, err := h.groupConfigs.SaveGroupConfig(cfg, h.runtime.Config())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.groups.save", "群配置已保存（控制台）", groupID, map[string]any{"group_id": groupID})
	c.JSON(http.StatusOK, gin.H{"config": saved.WithDefaults(groupID, h.runtime.Config())})
}
