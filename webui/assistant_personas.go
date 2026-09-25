// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 人设库的控制台接口。存的是几份具名的 SOUL.md，随时换。内置的几份编译在程序里，
// 列表里排最前面，只读。
//
// 这一层刻意和 BotConfig 完全解耦：库里存什么、机器人当前跑什么，是两回事。
// 「套用」发生在界面上——点一下把正文填进编辑框，用户看着它改、自己按保存。
// 因此这里没有 activate/current 的概念，库里改了也不会去动已经配好的机器人。

type personaSavePayload struct {
	Persona assistant.Persona `json:"persona"`
}

type personaDeletePayload struct {
	ID string `json:"id"`
}

// personaImportPayload 接的是一份 SOUL.md 的原文。Filename 用来在没有一级标题时
// 给这份人设起名。
type personaImportPayload struct {
	Source   string `json:"source"`
	Filename string `json:"filename,omitempty"`
}

type personaImportResponse struct {
	Personas []assistant.Persona `json:"personas"`
	Imported int                 `json:"imported"`
	Skipped  int                 `json:"skipped"`
	Renamed  int                 `json:"renamed"`
	Dropped  int                 `json:"dropped"`
	// UnknownStyles 让手写或跨版本的人设文件可排查：拼错的风格会被静默退回
	// 「助手」，不点名的话用户只会看到「导入成功」而语气完全不对。
	UnknownStyles []string `json:"unknown_styles,omitempty"`
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
	router.POST(base+"/personas/import", h.importPersonas)
}

func (h *BotHandler) loadPersonaSet(c *gin.Context) (assistant.PersonaSet, bool) {
	if h == nil || h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "personas", errPersonaStoreUnavailable, "", nil)
		return assistant.PersonaSet{}, false
	}
	set, _, err := h.sqlite.LoadBotPersonas(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "personas", err, "", nil)
		return assistant.PersonaSet{}, false
	}
	return set.WithDefaults(), true
}

// listPersonas 返回整个人设库：内置的在前，用户自己存的在后。
func (h *BotHandler) listPersonas(c *gin.Context) {
	set, ok := h.loadPersonaSet(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, personaListResponse{Personas: withBuiltinPersonas(set.Personas), Limit: assistant.PersonaLibraryMaxEntries})
}

// withBuiltinPersonas 把内置人设排在用户人设前面。每个返回整库的接口都走这里，
// 界面拿到的永远是同一张完整列表。
func withBuiltinPersonas(saved []assistant.Persona) []assistant.Persona {
	personas := assistant.BuiltinPersonas()
	for _, persona := range saved {
		if assistant.IsBuiltinPersonaID(persona.ID) {
			continue
		}
		personas = append(personas, persona)
	}
	return personas
}

// savePersona 新增或更新一套人设。带 ID 是改，不带是新增。
func (h *BotHandler) savePersona(c *gin.Context) {
	var payload personaSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_save", err, "", nil)
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
		h.writeError(c, http.StatusBadRequest, "personas_save", err, strings.TrimSpace(payload.Persona.Name), nil)
		return
	}
	if err := h.sqlite.SaveBotPersonas(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "personas_save", err, saved.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "personas_save", "人设已保存", saved.Name, map[string]any{"persona_id": saved.ID})
	c.JSON(http.StatusOK, gin.H{"persona": saved, "personas": withBuiltinPersonas(updated.Personas)})
}

// deletePersona 删掉一套人设。这只动库，不影响任何已经套用过它的机器人配置。
func (h *BotHandler) deletePersona(c *gin.Context) {
	var payload personaDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_delete", err, "", nil)
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
		h.writeError(c, http.StatusInternalServerError, "personas_delete", err, persona.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "personas_delete", "人设已删除", persona.Name, map[string]any{"persona_id": strings.TrimSpace(payload.ID)})
	c.JSON(http.StatusOK, gin.H{"personas": withBuiltinPersonas(updated.Personas)})
}

// importPersonas 把一份 SOUL.md 并进库里。
func (h *BotHandler) importPersonas(c *gin.Context) {
	var payload personaImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_import", err, "", nil)
		return
	}
	fallbackName := strings.TrimSuffix(filepath.Base(payload.Filename), filepath.Ext(payload.Filename))
	document, err := assistant.ParsePersonaMarkdown([]byte(payload.Source), fallbackName)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_import", err, "", nil)
		return
	}
	personaLibraryMu.Lock()
	defer personaLibraryMu.Unlock()

	set, ok := h.loadPersonaSet(c)
	if !ok {
		return
	}
	updated, result := set.Import(document.Personas, time.Now())
	if err := h.sqlite.SaveBotPersonas(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "personas_import", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "personas_import", "人设已导入", "", map[string]any{
		"imported":       len(result.Imported),
		"skipped":        result.Skipped,
		"renamed":        result.Renamed,
		"dropped":        result.Dropped,
		"unknown_styles": result.UnknownStyles,
	})
	c.JSON(http.StatusOK, personaImportResponse{
		Personas:      withBuiltinPersonas(updated.Personas),
		Imported:      len(result.Imported),
		Skipped:       result.Skipped,
		Renamed:       result.Renamed,
		Dropped:       result.Dropped,
		UnknownStyles: result.UnknownStyles,
	})
}

var (
	errPersonaStoreUnavailable = errors.New("人设库存储未配置")
	errPersonaImportEmpty      = errors.New("文件里没有可导入的人设")
)
