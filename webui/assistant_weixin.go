// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// 微信（iLink）扫码登录。凭据只能从这里写进配置：扫码确认后服务端才下发 token，
// 普通的配置保存接口不接受这几个字段。

type weixinLoginPayload struct {
	ProfileID  string `json:"profile_id"`
	SessionID  string `json:"session_id,omitempty"`
	VerifyCode string `json:"verify_code,omitempty"`
}

type weixinLoginResponse struct {
	assistant.WeixinLoginStatus
	// Config 只在登录成功、配置已经落库之后带回，前端用它刷新表单。
	Config *assistant.ConfigPayload `json:"config,omitempty"`
}

func (h *BotHandler) registerWeixinRoutes(router gin.IRouter, base string) {
	router.POST(base+"/weixin/login", h.startWeixinLogin)
	router.POST(base+"/weixin/login/poll", h.pollWeixinLogin)
	router.POST(base+"/weixin/logout", h.logoutWeixin)
}

func (h *BotHandler) weixinLoginManager() *assistant.WeixinLoginManager {
	h.weixinLoginOnce.Do(func() {
		if h.weixinLogin == nil {
			h.weixinLogin = assistant.NewWeixinLoginManager()
		}
	})
	return h.weixinLogin
}

// weixinProfile 找出要扫码的那台机器人，并确认它确实是微信平台。
func (h *BotHandler) weixinProfile(profileID string) (assistant.BotConfig, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return assistant.BotConfig{}, fmt.Errorf("请先保存这台微信机器人，再扫码登录")
	}
	cfg, ok := h.profiles.Profiles().ConfigForProfile(profileID)
	if !ok {
		return assistant.BotConfig{}, fmt.Errorf("profile %q not found", profileID)
	}
	if assistant.NormalizePlatformID(cfg.Platform) != assistant.PlatformWeixin {
		return assistant.BotConfig{}, fmt.Errorf("机器人「%s」不是微信平台", cfg.Name)
	}
	return cfg, nil
}

func (h *BotHandler) startWeixinLogin(c *gin.Context) {
	var payload weixinLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_login", err, "", nil)
		return
	}
	cfg, err := h.weixinProfile(payload.ProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_login", err, payload.ProfileID, nil)
		return
	}
	status, err := h.weixinLoginManager().Start(c.Request.Context(), cfg.ID, []string{cfg.WeixinBotToken})
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "weixin_login", err, cfg.ID, nil)
		return
	}
	c.JSON(http.StatusOK, weixinLoginResponse{WeixinLoginStatus: status})
}

func (h *BotHandler) pollWeixinLogin(c *gin.Context) {
	var payload weixinLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_login", err, "", nil)
		return
	}
	cfg, err := h.weixinProfile(payload.ProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_login", err, payload.ProfileID, nil)
		return
	}
	status, err := h.weixinLoginManager().Poll(c.Request.Context(), cfg.ID, payload.SessionID, payload.VerifyCode)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "weixin_login", err, cfg.ID, nil)
		return
	}
	if status.Credentials == nil {
		c.JSON(http.StatusOK, weixinLoginResponse{WeixinLoginStatus: status})
		return
	}
	saved, err := h.saveWeixinCredentials(cfg.ID, *status.Credentials)
	if err != nil {
		// 扫码会话已经用掉了，前端要重新发起；报错原文会显示在二维码位置。
		h.writeError(c, http.StatusBadRequest, "weixin_login", fmt.Errorf("扫码成功但保存失败：%w", err), cfg.ID, nil)
		return
	}
	recordRequestOperation(c, h.logs, "weixin_login", "微信扫码登录成功", cfg.ID, map[string]any{"profile_id": cfg.ID, "weixin_bot_id": status.Credentials.BotID})
	c.JSON(http.StatusOK, weixinLoginResponse{WeixinLoginStatus: status, Config: &saved})
}

// saveWeixinCredentials 把扫码结果写进配置并重建连接。
func (h *BotHandler) saveWeixinCredentials(profileID string, creds assistant.WeixinCredentials) (assistant.ConfigPayload, error) {
	return h.updateWeixinProfile(profileID, func(set assistant.ProfileSet, cfg *assistant.BotConfig) error {
		// 同一个微信号挂在两台机器人上，会有两个长轮询抢同一个游标，谁收到消息全凭运气。
		for _, other := range set.Profiles {
			if other.ID != cfg.ID && other.WeixinBotID != "" && other.WeixinBotID == creds.BotID {
				return fmt.Errorf("这个微信号已经绑定在机器人「%s」上，请先在那台解绑", other.Name)
			}
		}
		cfg.WeixinBotToken = creds.BotToken
		cfg.WeixinBotID = creds.BotID
		cfg.WeixinBaseURL = creds.BaseURL
		cfg.WeixinUserID = creds.UserID
		// 扫码的人几乎总是主人本人；没填主人时顺手填上，已填的不动。
		if strings.TrimSpace(cfg.OwnerID) == "" {
			cfg.OwnerID = creds.UserID
		}
		return nil
	})
}

func (h *BotHandler) logoutWeixin(c *gin.Context) {
	var payload weixinLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_logout", err, "", nil)
		return
	}
	cfg, err := h.weixinProfile(payload.ProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "weixin_logout", err, payload.ProfileID, nil)
		return
	}
	saved, err := h.updateWeixinProfile(cfg.ID, func(_ assistant.ProfileSet, cfg *assistant.BotConfig) error {
		cfg.WeixinBotToken = ""
		cfg.WeixinBotID = ""
		cfg.WeixinBaseURL = ""
		cfg.WeixinUserID = ""
		return nil
	})
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "weixin_logout", err, cfg.ID, nil)
		return
	}
	recordRequestOperation(c, h.logs, "weixin_logout", "微信登录已解绑", cfg.ID, map[string]any{"profile_id": cfg.ID})
	c.JSON(http.StatusOK, saved)
}

func (h *BotHandler) updateWeixinProfile(profileID string, mutate func(assistant.ProfileSet, *assistant.BotConfig) error) (assistant.ConfigPayload, error) {
	h.weixinSaveMu.Lock()
	defer h.weixinSaveMu.Unlock()
	set := h.profiles.Profiles()
	next := set
	next.Profiles = append([]assistant.BotConfig(nil), set.Profiles...)
	found := false
	for index := range next.Profiles {
		if next.Profiles[index].ID != profileID {
			continue
		}
		if err := mutate(set, &next.Profiles[index]); err != nil {
			return assistant.ConfigPayload{}, err
		}
		found = true
		break
	}
	if !found {
		return assistant.ConfigPayload{}, fmt.Errorf("profile %q not found", profileID)
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		return assistant.ConfigPayload{}, err
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		return assistant.ConfigPayload{}, err
	}
	return assistant.PayloadFromProfileSet(next, profileID), nil
}
