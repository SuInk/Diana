// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// 词典的控制台接口。词典平时由机器人自己维护，但「自己维护」不等于「没人看得见」：
// 记错了一个梗、把一句玩笑当成词条收进去，群里没人会专门去纠正它，只能靠人在控制台
// 翻一眼、改掉或作废。这里提供的就是这层人工兜底，写入路径和工具走同一套存储，
// 因此同样会留修订记录——控制台改的也算一次修订，不是绕过历史的后门。

const glossaryConsoleListLimit = 200

type glossaryEntryRequest struct {
	Scope   string   `json:"scope"`
	Term    string   `json:"term"`
	Aliases []string `json:"aliases"`
	Meaning string   `json:"meaning"`
	Example string   `json:"example"`
	Note    string   `json:"note"`
}

type glossaryListResponse struct {
	Scopes  []storage.GlossaryScopeSummary `json:"scopes"`
	Scope   string                         `json:"scope"`
	Entries []assistant.GlossaryEntry      `json:"entries"`
	Query   string                         `json:"query,omitempty"`
}

// listGlossary 返回作用域清单和选中作用域下的词条。
func (h *BotHandler) listGlossary(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "词典存储未配置"})
		return
	}
	scopes, err := h.sqlite.ListGlossaryScopes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scope := strings.TrimSpace(c.Query("scope"))
	if scope == "" && len(scopes) > 0 {
		scope = scopes[0].ScopeKey
	}
	response := glossaryListResponse{Scopes: scopes, Scope: scope, Query: strings.TrimSpace(c.Query("q"))}
	if scope == "" {
		response.Entries = []assistant.GlossaryEntry{}
		c.JSON(http.StatusOK, response)
		return
	}
	includeDeleted, _ := strconv.ParseBool(c.DefaultQuery("include_deleted", "false"))
	entries, err := h.sqlite.ListGlossaryEntries(c.Request.Context(), assistant.GlossaryQuery{
		ScopeKeys:      []string{scope},
		Text:           response.Query,
		Limit:          glossaryConsoleListLimit,
		IncludeDeleted: includeDeleted,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []assistant.GlossaryEntry{}
	}
	response.Entries = entries
	c.JSON(http.StatusOK, response)
}

// getGlossaryEntry 返回单条词条及修订记录。修订记录是这个页面存在的理由之一：
// 「这个词什么时候被改成现在这个意思的」在别处看不到。
func (h *BotHandler) getGlossaryEntry(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "词典存储未配置"})
		return
	}
	scope := strings.TrimSpace(c.Query("scope"))
	term := strings.TrimSpace(c.Query("term"))
	if scope == "" || term == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少作用域或词条"})
		return
	}
	entry, found, err := h.sqlite.GlossaryEntryDetail(c.Request.Context(), scope, term)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "词条不存在"})
		return
	}
	c.JSON(http.StatusOK, entry)
}

// saveGlossaryEntry 新建或修订一条词条。
func (h *BotHandler) saveGlossaryEntry(c *gin.Context) {
	request, ok := h.bindGlossaryRequest(c)
	if !ok {
		return
	}
	if strings.TrimSpace(request.Meaning) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "释义不能为空"})
		return
	}
	entry, created, err := h.sqlite.UpsertGlossaryEntry(c.Request.Context(), assistant.GlossaryUpsertRequest{
		ScopeKey: request.Scope,
		Term:     assistant.TruncateGlossaryText(request.Term, assistant.GlossaryTermMaxRunes),
		Aliases:  request.Aliases,
		Meaning:  assistant.TruncateGlossaryText(request.Meaning, assistant.GlossaryMeaningMaxRunes),
		Example:  assistant.TruncateGlossaryText(request.Example, assistant.GlossaryExampleMaxRunes),
		Note:     assistant.TruncateGlossaryText(request.Note, assistant.GlossaryNoteMaxRunes),
		// 控制台的改动记在控制台名下，别冒充群里某个人改的。
		EditorName: "控制台",
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	action := "assistant.glossary.update"
	message := "词典词条已更新"
	if created {
		action, message = "assistant.glossary.create", "词典词条已新增"
	}
	recordRequestOperation(c, h.logs, action, message, entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term, "version": entry.Version})
	c.JSON(http.StatusOK, entry)
}

// deleteGlossaryEntry 作废一条词条。软删除：修订记录留着，恢复得回来。
func (h *BotHandler) deleteGlossaryEntry(c *gin.Context) {
	request, ok := h.bindGlossaryRequest(c)
	if !ok {
		return
	}
	entry, found, err := h.sqlite.DeleteGlossaryEntry(c.Request.Context(), request.Scope, request.Term,
		"", "控制台", assistant.TruncateGlossaryText(request.Note, assistant.GlossaryNoteMaxRunes), time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "词条不存在"})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.glossary.delete", "词典词条已作废", entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term})
	c.JSON(http.StatusOK, entry)
}

// restoreGlossaryEntry 撤销一次作废。
func (h *BotHandler) restoreGlossaryEntry(c *gin.Context) {
	request, ok := h.bindGlossaryRequest(c)
	if !ok {
		return
	}
	entry, found, err := h.sqlite.RestoreGlossaryEntry(c.Request.Context(), request.Scope, request.Term, "", "控制台", time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "词条不存在"})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.glossary.restore", "词典词条已恢复", entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term})
	c.JSON(http.StatusOK, entry)
}

func (h *BotHandler) bindGlossaryRequest(c *gin.Context) (glossaryEntryRequest, bool) {
	var request glossaryEntryRequest
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "词典存储未配置"})
		return request, false
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return request, false
	}
	request.Scope = strings.TrimSpace(request.Scope)
	request.Term = strings.TrimSpace(request.Term)
	if request.Scope == "" || request.Term == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少作用域或词条"})
		return request, false
	}
	return request, true
}
