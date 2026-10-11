// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type replyToneSavePayload struct {
	Preset assistant.ReplyTonePreset `json:"preset"`
}

type replyToneDeletePayload struct {
	ID string `json:"id"`
}

type replyToneListResponse struct {
	Presets []assistant.ReplyTonePreset `json:"presets"`
	Limit   int                         `json:"limit"`
}

var replyToneLibraryMu sync.Mutex

func (h *BotHandler) registerReplyToneRoutes(router gin.IRouter, base string) {
	router.GET(base+"/reply-tones", h.listReplyTones)
	router.POST(base+"/reply-tones", h.saveReplyTone)
	router.POST(base+"/reply-tones/delete", h.deleteReplyTone)
}

func (h *BotHandler) loadReplyToneSet(c *gin.Context) (assistant.ReplyToneSet, bool) {
	if h == nil || h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "reply_tones", errReplyToneStoreUnavailable, "", nil)
		return assistant.ReplyToneSet{}, false
	}
	set, _, err := h.sqlite.LoadBotReplyTones(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "reply_tones", err, "", nil)
		return assistant.ReplyToneSet{}, false
	}
	return set.WithDefaults(), true
}

func withBuiltinReplyTones(saved []assistant.ReplyTonePreset) []assistant.ReplyTonePreset {
	presets := assistant.BuiltinReplyTones()
	for _, preset := range saved {
		if !assistant.IsBuiltinReplyToneID(preset.ID) {
			presets = append(presets, preset)
		}
	}
	return presets
}

func (h *BotHandler) listReplyTones(c *gin.Context) {
	set, ok := h.loadReplyToneSet(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, replyToneListResponse{Presets: withBuiltinReplyTones(set.Presets), Limit: assistant.ReplyToneLibraryMaxEntries})
}

func (h *BotHandler) saveReplyTone(c *gin.Context) {
	var payload replyToneSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "reply_tones_save", err, "", nil)
		return
	}
	replyToneLibraryMu.Lock()
	defer replyToneLibraryMu.Unlock()
	set, ok := h.loadReplyToneSet(c)
	if !ok {
		return
	}
	updated, saved, err := set.Save(payload.Preset, time.Now())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "reply_tones_save", err, strings.TrimSpace(payload.Preset.Name), nil)
		return
	}
	if err := h.sqlite.SaveBotReplyTones(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "reply_tones_save", err, saved.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "reply_tones_save", "回复口吻已保存", saved.Name, map[string]any{"preset_id": saved.ID})
	c.JSON(http.StatusOK, gin.H{"preset": saved, "presets": withBuiltinReplyTones(updated.Presets)})
}

func (h *BotHandler) deleteReplyTone(c *gin.Context) {
	var payload replyToneDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "reply_tones_delete", err, "", nil)
		return
	}
	if assistant.IsBuiltinReplyToneID(payload.ID) {
		h.writeError(c, http.StatusBadRequest, "reply_tones_delete", errors.New("内置回复口吻只读"), "", nil)
		return
	}
	replyToneLibraryMu.Lock()
	defer replyToneLibraryMu.Unlock()
	set, ok := h.loadReplyToneSet(c)
	if !ok {
		return
	}
	preset, _ := set.Find(payload.ID)
	updated := set.Delete(payload.ID)
	if err := h.sqlite.SaveBotReplyTones(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "reply_tones_delete", err, preset.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "reply_tones_delete", "回复口吻已删除", preset.Name, map[string]any{"preset_id": strings.TrimSpace(payload.ID)})
	c.JSON(http.StatusOK, gin.H{"presets": withBuiltinReplyTones(updated.Presets)})
}

var errReplyToneStoreUnavailable = errors.New("回复口吻库存储未配置")
