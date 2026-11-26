// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/assistant"
)

func (h *BotHandler) resolveMobileSocialPlugin(c *gin.Context, action string) (*assistant.MobileSocialPlugin, bool) {
	pluginVal, _, ok := h.runtime.Plugins().PluginForConfiguration(assistant.MobileSocialPluginID)
	if !ok {
		h.writeError(c, http.StatusNotFound, action, assistant.ErrPluginNotFound, assistant.MobileSocialPluginID, nil)
		return nil, false
	}
	mobileSocial, ok := pluginVal.(*assistant.MobileSocialPlugin)
	if !ok || mobileSocial == nil {
		h.writeError(c, http.StatusInternalServerError, action, errors.New("mobile-social plugin has unexpected implementation"), assistant.MobileSocialPluginID, nil)
		return nil, false
	}
	return mobileSocial, true
}

func (h *BotHandler) testMobileSocialConnection(c *gin.Context) {
	plugin, settings, ok := h.pluginTestSettings(c, assistant.MobileSocialPluginID, "plugin_mobile_social_test")
	if !ok {
		return
	}
	mobileSocial, ok := plugin.(*assistant.MobileSocialPlugin)
	if !ok {
		h.writeError(c, http.StatusInternalServerError, "plugin_mobile_social_test", errors.New("mobile-social plugin has unexpected implementation"), assistant.MobileSocialPluginID, nil)
		return
	}
	c.JSON(http.StatusOK, mobileSocial.TestConnections(c.Request.Context(), settings))
}

func (h *BotHandler) getMobileSocialState(c *gin.Context) {
	mobileSocial, ok := h.resolveMobileSocialPlugin(c, "plugin_mobile_social_state")
	if !ok {
		return
	}
	c.JSON(http.StatusOK, mobileSocial.StateSnapshot())
}

func (h *BotHandler) runMobileSocialPatrol(c *gin.Context) {
	plugin, settings, ok := h.pluginTestSettings(c, assistant.MobileSocialPluginID, "plugin_mobile_social_patrol")
	if !ok {
		return
	}
	mobileSocial, ok := plugin.(*assistant.MobileSocialPlugin)
	if !ok {
		h.writeError(c, http.StatusInternalServerError, "plugin_mobile_social_patrol", errors.New("mobile-social plugin has unexpected implementation"), assistant.MobileSocialPluginID, nil)
		return
	}
	res, err := mobileSocial.PatrolOnceWithSettings(c.Request.Context(), settings)
	if err != nil && res.FetchedCount == 0 {
		h.writeError(c, http.StatusBadGateway, "plugin_mobile_social_patrol", err, assistant.MobileSocialPluginID, nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"result": res,
		"state":  mobileSocial.StateSnapshot(),
	})
}

func (h *BotHandler) deleteMobileSocialCandidate(c *gin.Context) {
	mobileSocial, ok := h.resolveMobileSocialPlugin(c, "plugin_mobile_social_delete_candidate")
	if !ok {
		return
	}
	removed := mobileSocial.DeleteCandidate(c.Param("id"))
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "候选条目不存在或已过期"})
		return
	}
	c.JSON(http.StatusOK, mobileSocial.StateSnapshot())
}
