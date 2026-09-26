// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// vrchatStatus 返回 OSC 桥的实时状态，插件设置页据此显示连没连上、当前 Avatar
// 和映射表的解析问题。桥是进程级的，不按机器人区分。
func (h *BotHandler) vrchatStatus(c *gin.Context) {
	plugin, _, ok := h.runtime.Plugins().PluginForConfiguration(assistant.VRChatPluginID)
	if !ok {
		h.writeError(c, http.StatusNotFound, "plugin_vrchat_status", assistant.ErrPluginNotFound, assistant.VRChatPluginID, nil)
		return
	}
	vrchat, ok := plugin.(*assistant.VRChatPlugin)
	if !ok {
		h.writeError(c, http.StatusInternalServerError, "plugin_vrchat_status", errors.New("vrchat plugin has unexpected implementation"), assistant.VRChatPluginID, nil)
		return
	}
	c.JSON(http.StatusOK, vrchat.Status())
}
