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
// 因此这里没有 activate/current 的概念。机器人和群可以绑定库里的一套
// （persona_id）：库保存或删除时把变化写到绑定它的机器人和群，见 persona_link.go。
// 见 persona_library.go 顶部关于「套用来源而不是活绑定」的那段。

type personaSavePayload struct {
	Persona assistant.Persona `json:"persona"`
}

type personaDeletePayload struct {
	ID string `json:"id"`
}

// personaImportPayload 接的是人设文件的原文，YAML 只在服务端解析，按文件里的
// format_version 选规则。以前还收前端解析好的 personas 数组，那条路绕开了格式检查，
// 旧格式不再兼容，一并去掉。
type personaImportPayload struct {
	Source string `json:"source"`
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
	router.POST(base+"/personas/yaml", h.renderPersonaYAML)
	router.POST(base+"/personas/parse", h.parsePersonaSource)
}

type personaYAMLPayload struct {
	Personas []assistant.Persona `json:"personas"`
}

// renderPersonaYAML 把人设渲染成 YAML，给导出分享和 YAML 编辑器用。YAML 只在服务端
// 生成和解析：前端没有 YAML 库，两头各写一份迟早对不上。
func (h *BotHandler) renderPersonaYAML(c *gin.Context) {
	var payload personaYAMLPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_yaml", err, "", nil)
		return
	}
	if len(payload.Personas) == 0 {
		h.writeError(c, http.StatusBadRequest, "personas_yaml", errPersonaImportEmpty, "", nil)
		return
	}
	out, err := assistant.RenderPersonaYAML(payload.Personas)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "personas_yaml", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"yaml": string(out)})
}

// parsePersonaSource 只解析不入库：YAML 编辑器点「应用」时把结果填回表单，
// 保存与否仍由用户按保存决定。
func (h *BotHandler) parsePersonaSource(c *gin.Context) {
	var payload personaImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_parse", err, "", nil)
		return
	}
	document, err := assistant.ParsePersonaDocument([]byte(payload.Source))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_parse", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"personas": document.Personas})
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
	// 绑定这一套的机器人和群跟着更新。库已经存好了，同步失败不回滚库，只报出来。
	botsSynced, botErr := h.syncBotsLinkedToPersona(saved)
	groupsSynced, groupErr := h.syncGroupsLinkedToPersona(saved)
	recordRequestOperation(c, h.logs, "personas_save", "人设已保存", saved.Name, map[string]any{"persona_id": saved.ID, "bots_synced": botsSynced, "groups_synced": groupsSynced})
	response := gin.H{"persona": saved, "personas": updated.Personas, "bots_synced": botsSynced, "groups_synced": groupsSynced}
	if err := errors.Join(botErr, groupErr); err != nil {
		response["warning"] = "人设已保存，但同步到绑定它的机器人或群时失败：" + err.Error()
	}
	c.JSON(http.StatusOK, response)
}

// deletePersona 删掉一套人设。绑定它的机器人和群保留现有人设、解除绑定。
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
	// 绑定它的机器人和群保留现有人设，解除绑定。
	botsUnlinked, botErr := h.unlinkBotsFromPersona(payload.ID)
	groupsUnlinked, groupErr := h.unlinkGroupsFromPersona(payload.ID)
	recordRequestOperation(c, h.logs, "personas_delete", "人设已删除", persona.Name, map[string]any{"persona_id": strings.TrimSpace(payload.ID), "bots_unlinked": botsUnlinked, "groups_unlinked": groupsUnlinked})
	response := gin.H{"personas": updated.Personas, "bots_unlinked": botsUnlinked, "groups_unlinked": groupsUnlinked}
	if err := errors.Join(botErr, groupErr); err != nil {
		response["warning"] = "人设已删除，但解除绑定时失败：" + err.Error()
	}
	c.JSON(http.StatusOK, response)
}

// importPersonas 把导出文件并进库里。合并在这里做而不是前端逐条 POST：一次读改写
// 落一次库，中途失败不会留下「导了一半」的状态，也省掉 N 次往返。
func (h *BotHandler) importPersonas(c *gin.Context) {
	var payload personaImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "personas_import", err, "", nil)
		return
	}
	document, err := assistant.ParsePersonaDocument([]byte(payload.Source))
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
	personas := updated.Personas
	if personas == nil {
		personas = []assistant.Persona{}
	}
	c.JSON(http.StatusOK, personaImportResponse{
		Personas:      personas,
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
