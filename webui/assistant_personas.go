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

// 人设库的控制台接口。存的是「它是谁、怎么说话」的几套具名组合，随时换。
//
// 这一层刻意和 BotConfig 完全解耦：库里存什么、机器人当前跑什么，是两回事。
// 「套用」发生在界面上——点一下把四个字段填进表单，用户看着它改、自己按保存。
// 因此这里没有任何 activate/current 的概念，也不需要在 BotConfig 上挂 persona_id。
// 见 persona_library.go 顶部关于「套用来源而不是活绑定」的那段。

type personaSavePayload struct {
	Persona assistant.Persona `json:"persona"`
}

type personaDeletePayload struct {
	ID string `json:"id"`
}

type personaListResponse struct {
	Personas []assistant.Persona `json:"personas"`
	Limit    int                 `json:"limit"`
}

// personaLibraryMu 串行化「读改写」。这份数据整块存整块写，两个请求同时保存会
// 让后写的那份把前一份的新增覆盖掉。
var personaLibraryMu sync.Mutex

func (h *BotHandler) registerPersonaRoutes(router gin.IRouter, base string) {
	router.GET(base+"/personas", h.listPersonas)
	router.POST(base+"/personas", h.savePersona)
	router.POST(base+"/personas/delete", h.deletePersona)
}

func (h *BotHandler) loadPersonaSet(c *gin.Context) (assistant.PersonaSet, bool) {
	if h == nil || h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.personas", errPersonaStoreUnavailable, "", nil)
		return assistant.PersonaSet{}, false
	}
	set, _, err := h.sqlite.LoadBotPersonas(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.personas", err, "", nil)
		return assistant.PersonaSet{}, false
	}
	return set.WithDefaults(), true
}

// listPersonas 返回整个人设库。
func (h *BotHandler) listPersonas(c *gin.Context) {
	set, ok := h.loadPersonaSet(c)
	if !ok {
		return
	}
	personas := set.Personas
	if personas == nil {
		personas = []assistant.Persona{}
	}
	c.JSON(http.StatusOK, personaListResponse{Personas: personas, Limit: assistant.PersonaLibraryMaxEntries})
}

// savePersona 新增或更新一套人设。带 ID 是改，不带是新增。
func (h *BotHandler) savePersona(c *gin.Context) {
	var payload personaSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.save", err, "", nil)
		return
	}
	personaLibraryMu.Lock()
	defer personaLibraryMu.Unlock()

	set, ok := h.loadPersonaSet(c)
	if !ok {
		return
	}
	updated, saved, err := set.Save(payload.Persona, time.Now())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.save", err, strings.TrimSpace(payload.Persona.Name), nil)
		return
	}
	if err := h.sqlite.SaveBotPersonas(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.personas.save", err, saved.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.personas.save", "人设已保存", saved.Name, map[string]any{"persona_id": saved.ID})
	c.JSON(http.StatusOK, gin.H{"persona": saved, "personas": updated.Personas})
}

// deletePersona 删掉一套人设。这只动库，不影响任何已经套用过它的机器人配置。
func (h *BotHandler) deletePersona(c *gin.Context) {
	var payload personaDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.delete", err, "", nil)
		return
	}
	personaLibraryMu.Lock()
	defer personaLibraryMu.Unlock()

	set, ok := h.loadPersonaSet(c)
	if !ok {
		return
	}
	persona, _ := set.Find(payload.ID)
	updated := set.Delete(payload.ID)
	if err := h.sqlite.SaveBotPersonas(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.personas.delete", err, persona.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.personas.delete", "人设已删除", persona.Name, map[string]any{"persona_id": strings.TrimSpace(payload.ID)})
	c.JSON(http.StatusOK, gin.H{"personas": updated.Personas})
}

var errPersonaStoreUnavailable = errors.New("人设库存储未配置")
