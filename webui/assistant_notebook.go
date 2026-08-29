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

// 笔记本的控制台接口。笔记本平时由机器人自己维护，但「自己维护」不等于「没人看得见」：
// 记错了一个梗、把一句玩笑当成条目收进去，群里没人会专门去纠正它，只能靠人在控制台
// 翻一眼、改掉或作废。这里提供的就是这层人工兜底，写入路径和工具走同一套存储，
// 因此同样会留修订记录——控制台改的也算一次修订，不是绕过历史的后门。

const notebookConsoleListLimit = 200

type notebookEntryRequest struct {
	Scope   string   `json:"scope"`
	Kind    string   `json:"kind"`
	Term    string   `json:"term"`
	Aliases []string `json:"aliases"`
	Meaning string   `json:"meaning"`
	Example string   `json:"example"`
	Note    string   `json:"note"`
}

// notebookKindOption 是控制台上的类型筛选项。类型名和中文标签都由后端给出，
// 前端不再自己维护一份——两处各写一份，加类型时必然漏一处。
type notebookKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func notebookKindOptions() []notebookKindOption {
	kinds := assistant.NotebookKinds()
	options := make([]notebookKindOption, 0, len(kinds))
	for _, kind := range kinds {
		options = append(options, notebookKindOption{Value: string(kind), Label: kind.Label()})
	}
	return options
}

type notebookListResponse struct {
	Scopes  []storage.NotebookScopeSummary `json:"scopes"`
	Scope   string                         `json:"scope"`
	Kinds   []notebookKindOption           `json:"kinds"`
	Kind    string                         `json:"kind,omitempty"`
	Entries []assistant.NotebookEntry      `json:"entries"`
	Query   string                         `json:"query,omitempty"`
}

// listNotebook 返回作用域清单和选中作用域下的条目。
func (h *BotHandler) listNotebook(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "笔记本存储未配置"})
		return
	}
	scopes, err := h.sqlite.ListNotebookScopes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scopes = h.filterNotebookScopesForBot(scopes, botProfileScope(c))
	scope := strings.TrimSpace(c.Query("scope"))
	if scope == "" && len(scopes) > 0 {
		scope = scopes[0].ScopeKey
	}
	response := notebookListResponse{
		Scopes: scopes,
		Scope:  scope,
		Query:  strings.TrimSpace(c.Query("q")),
		Kinds:  notebookKindOptions(),
		Kind:   strings.TrimSpace(c.Query("kind")),
	}
	if scope == "" {
		response.Entries = []assistant.NotebookEntry{}
		c.JSON(http.StatusOK, response)
		return
	}
	includeDeleted, _ := strconv.ParseBool(c.DefaultQuery("include_deleted", "false"))
	entries, err := h.sqlite.ListNotebookEntries(c.Request.Context(), assistant.NotebookQuery{
		ScopeKeys:      []string{scope},
		Kinds:          notebookKindFilterFromQuery(response.Kind),
		Text:           response.Query,
		Limit:          notebookConsoleListLimit,
		IncludeDeleted: includeDeleted,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []assistant.NotebookEntry{}
	}
	response.Entries = entries
	c.JSON(http.StatusOK, response)
}

// filterNotebookScopesForBot 按控制台选中的机器人裁掉别的机器人的作用域。
//
// 作用域键有三种来源：会话键（开了平台上下文隔离时前缀是配置档 ID）、每台机器人
// 各一本的全局笔记本（bot:<id>）、以及升级前那本共用的 global。判定方式是「排除法」
// 而不是「白名单」：只把明确属于别的配置档的键去掉，剩下的都留着。没开隔离时会话键
// 本来就没有前缀，两台机器人读的是同一份，白名单会把它们全藏起来。
func (h *BotHandler) filterNotebookScopesForBot(scopes []storage.NotebookScopeSummary, botProfileID string) []storage.NotebookScopeSummary {
	botProfileID = strings.TrimSpace(botProfileID)
	if botProfileID == "" || h.profiles == nil {
		return scopes
	}
	others := make([]string, 0, 4)
	for _, profile := range h.profiles.Profiles().Profiles {
		id := strings.TrimSpace(profile.ID)
		if id != "" && id != botProfileID {
			others = append(others, id)
		}
	}
	if len(others) == 0 {
		return scopes
	}
	filtered := make([]storage.NotebookScopeSummary, 0, len(scopes))
	for _, summary := range scopes {
		if !notebookScopeOwnedByOther(summary.ScopeKey, others) {
			filtered = append(filtered, summary)
		}
	}
	return filtered
}

// notebookScopeOwnedByOther 判断一个作用域键是否明确属于列出的别的配置档。
func notebookScopeOwnedByOther(scopeKey string, others []string) bool {
	scopeKey = strings.TrimSpace(scopeKey)
	for _, id := range others {
		if scopeKey == assistant.NotebookScopeBotPrefix+id || strings.HasPrefix(scopeKey, id+":") {
			return true
		}
	}
	return false
}

// getNotebookEntry 返回单条条目及修订记录。修订记录是这个页面存在的理由之一：
// 「这个词什么时候被改成现在这个意思的」在别处看不到。
func (h *BotHandler) getNotebookEntry(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "笔记本存储未配置"})
		return
	}
	scope := strings.TrimSpace(c.Query("scope"))
	term := strings.TrimSpace(c.Query("term"))
	if scope == "" || term == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少作用域或条目"})
		return
	}
	entry, found, err := h.sqlite.NotebookEntryDetail(c.Request.Context(), scope, term)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "条目不存在"})
		return
	}
	c.JSON(http.StatusOK, entry)
}

// saveNotebookEntry 新建或修订一条条目。
func (h *BotHandler) saveNotebookEntry(c *gin.Context) {
	request, ok := h.bindNotebookRequest(c)
	if !ok {
		return
	}
	if strings.TrimSpace(request.Meaning) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "释义不能为空"})
		return
	}
	kind := assistant.NormalizeNotebookKind(request.Kind)
	entry, created, err := h.sqlite.UpsertNotebookEntry(c.Request.Context(), assistant.NotebookUpsertRequest{
		ScopeKey: request.Scope,
		Kind:     kind,
		// 标题上限按类型走：词条仍然只放得下一个词，其余类型要放得下一句概括。
		Term:    assistant.TruncateNotebookText(request.Term, kind.TitleLimit()),
		Aliases: request.Aliases,
		Meaning: assistant.TruncateNotebookText(request.Meaning, assistant.NotebookContentMaxRunes),
		Example: assistant.TruncateNotebookText(request.Example, assistant.NotebookExampleMaxRunes),
		Note:    assistant.TruncateNotebookText(request.Note, assistant.NotebookNoteMaxRunes),
		// 控制台的改动记在控制台名下，别冒充群里某个人改的。
		EditorName: "控制台",
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	action := "assistant.notebook.update"
	message := "笔记本条目已更新"
	if created {
		action, message = "assistant.notebook.create", "笔记本条目已新增"
	}
	recordRequestOperation(c, h.logs, action, message, entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term, "version": entry.Version})
	c.JSON(http.StatusOK, entry)
}

// deleteNotebookEntry 作废一条条目。软删除：修订记录留着，恢复得回来。
func (h *BotHandler) deleteNotebookEntry(c *gin.Context) {
	request, ok := h.bindNotebookRequest(c)
	if !ok {
		return
	}
	entry, found, err := h.sqlite.DeleteNotebookEntry(c.Request.Context(), request.Scope, request.Term,
		"", "控制台", assistant.TruncateNotebookText(request.Note, assistant.NotebookNoteMaxRunes), time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "条目不存在"})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.notebook.delete", "笔记本条目已作废", entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term})
	c.JSON(http.StatusOK, entry)
}

// restoreNotebookEntry 撤销一次作废。
func (h *BotHandler) restoreNotebookEntry(c *gin.Context) {
	request, ok := h.bindNotebookRequest(c)
	if !ok {
		return
	}
	entry, found, err := h.sqlite.RestoreNotebookEntry(c.Request.Context(), request.Scope, request.Term, "", "控制台", time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "条目不存在"})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.notebook.restore", "笔记本条目已恢复", entry.Term,
		map[string]any{"scope": entry.ScopeKey, "term": entry.Term})
	c.JSON(http.StatusOK, entry)
}

func (h *BotHandler) bindNotebookRequest(c *gin.Context) (notebookEntryRequest, bool) {
	var request notebookEntryRequest
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "笔记本存储未配置"})
		return request, false
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return request, false
	}
	request.Scope = strings.TrimSpace(request.Scope)
	request.Term = strings.TrimSpace(request.Term)
	if request.Scope == "" || request.Term == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少作用域或条目"})
		return request, false
	}
	return request, true
}

// notebookKindFilterFromQuery 把控制台的类型筛选转成查询条件。
// 空值和未知值都表示不筛，而不是筛出空列表——那会让人以为笔记丢了。
func notebookKindFilterFromQuery(raw string) []assistant.NotebookKind {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	kind := assistant.NotebookKind(strings.ToLower(trimmed))
	if !kind.Valid() {
		return nil
	}
	return []assistant.NotebookKind{kind}
}
