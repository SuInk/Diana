// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 自述的只读与清理接口。
//
// 写入只有机器人自己能做（对话里的 self_note 工具）：这一层的意义就是它自己记下
// 的观察，人代笔写进去的应该进 SOUL.md。所以这里只有列出、删一条和清空——
// 主人要能看见它给自己写了什么，也要能把不对的抹掉。
func (h *BotHandler) registerSelfNoteRoutes(router gin.IRouter, base string) {
	router.GET(base+"/self-notes", h.listSelfNotes)
	router.POST(base+"/self-notes/delete", h.deleteSelfNote)
	router.POST(base+"/self-notes/purge", h.purgeSelfNotes)
}

type selfNoteListResponse struct {
	Notes []assistant.SelfNote `json:"notes"`
	// Enabled 说明这台机器人有没有开自述。关着时列表是空的，但「空」和「没开」
	// 是两件事：前者是它还没写过，后者是它根本写不了。
	Enabled bool `json:"enabled"`
}

// selfNoteEnabledForProfile 读开关。配置里是 *bool，缺省关闭。
func selfNoteEnabledForProfile(cfg assistant.BotConfig) bool {
	return cfg.SelfNoteEnabled != nil && *cfg.SelfNoteEnabled
}

type selfNoteDeletePayload struct {
	ID string `json:"id"`
}

func (h *BotHandler) selfNoteProfileID(c *gin.Context) string {
	if profileID := botProfileScope(c); strings.TrimSpace(profileID) != "" {
		return strings.TrimSpace(profileID)
	}
	// 没带机器人参数时落到当前这台：单机器人部署不该被逼着先去查一个 ID。
	return strings.TrimSpace(h.runtime.ProfileConfig("").ID)
}

func (h *BotHandler) listSelfNotes(c *gin.Context) {
	profileID := h.selfNoteProfileID(c)
	response := selfNoteListResponse{
		Notes:   []assistant.SelfNote{},
		Enabled: selfNoteEnabledForProfile(h.runtime.ProfileConfig(profileID)),
	}
	// 带上历史版本：改写过的和删掉的都留着，主人要看的正是「它后来怎么改了这条」。
	notes, err := h.sqlite.ListSelfNotes(c.Request.Context(), profileID, queryBool(c.Query("include_inactive")), 0)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "self_notes_list", err, "", nil)
		return
	}
	if notes != nil {
		response.Notes = notes
	}
	c.JSON(http.StatusOK, response)
}

func (h *BotHandler) deleteSelfNote(c *gin.Context) {
	var payload selfNoteDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "self_notes_delete", err, "", nil)
		return
	}
	profileID := h.selfNoteProfileID(c)
	note, found, err := h.sqlite.DeleteSelfNote(c.Request.Context(), profileID, strings.TrimSpace(payload.ID),
		"", "控制台", time.Now())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "self_notes_delete", err, payload.ID, nil)
		return
	}
	if found {
		recordRequestOperation(c, h.logs, "self_notes_delete", "自述已删除", note.ID, map[string]any{
			"topic": note.Topic, "content": note.Content,
		})
	}
	h.listSelfNotes(c)
}

func (h *BotHandler) purgeSelfNotes(c *gin.Context) {
	profileID := h.selfNoteProfileID(c)
	removed, err := h.sqlite.PurgeSelfNotes(c.Request.Context(), profileID, "", "控制台", time.Now())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "self_notes_purge", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "self_notes_purge", "自述已清空", profileID, map[string]any{"removed": removed})
	h.listSelfNotes(c)
}
