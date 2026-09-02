// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 世界书（世界观设定库）的控制台接口。树是全局一棵，节点在这里增删改；
// 每台机器人用不用它由各自配置里的 world_book_enabled 决定。
// 存取模式与人设库一致：整块读、整块写，读改写用互斥锁串行化。

type worldBookSavePayload struct {
	Node assistant.WorldBookNode `json:"node"`
}

type worldBookDeletePayload struct {
	ID string `json:"id"`
}

// worldBookImportPayload 接的是导出文件的内容。Version 目前只用来在格式变了
// 以后认出旧文件。Entries 是 SillyTavern 世界书/角色卡 character_book 的原始
// 条目，前端认出那种文件时原样递进来，由后端统一转换——转换规则只维护一份。
type worldBookImportPayload struct {
	Version int                       `json:"version,omitempty"`
	Nodes   []assistant.WorldBookNode `json:"nodes"`
	Entries json.RawMessage           `json:"entries,omitempty"`
}

type worldBookListResponse struct {
	Nodes []assistant.WorldBookNode `json:"nodes"`
	Limit int                       `json:"limit"`
}

type worldBookImportResponse struct {
	Nodes    []assistant.WorldBookNode `json:"nodes"`
	Imported int                       `json:"imported"`
	Dropped  int                       `json:"dropped"`
}

// worldBookMu 串行化「读改写」。整块存整块写，两个请求同时保存会让后写的那份
// 把前一份的新增覆盖掉。
var worldBookMu sync.Mutex

func (h *BotHandler) registerWorldBookRoutes(router gin.IRouter, base string) {
	router.GET(base+"/world-book", h.listWorldBook)
	router.POST(base+"/world-book", h.saveWorldBookNode)
	router.POST(base+"/world-book/delete", h.deleteWorldBookNode)
	router.POST(base+"/world-book/import", h.importWorldBook)
}

func (h *BotHandler) loadWorldBook(c *gin.Context) (assistant.WorldBook, bool) {
	if h == nil || h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.world_book", errWorldBookStoreUnavailable, "", nil)
		return assistant.WorldBook{}, false
	}
	tree, _, err := h.sqlite.LoadWorldBook(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_book", err, "", nil)
		return assistant.WorldBook{}, false
	}
	return tree.WithDefaults(), true
}

// listWorldBook 返回整棵树。
func (h *BotHandler) listWorldBook(c *gin.Context) {
	tree, ok := h.loadWorldBook(c)
	if !ok {
		return
	}
	nodes := tree.Nodes
	if nodes == nil {
		nodes = []assistant.WorldBookNode{}
	}
	c.JSON(http.StatusOK, worldBookListResponse{Nodes: nodes, Limit: assistant.WorldBookMaxNodes})
}

// saveWorldBookNode 新增或更新一个节点。带 ID 是改，不带是新增。
func (h *BotHandler) saveWorldBookNode(c *gin.Context) {
	var payload worldBookSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_book.save", err, "", nil)
		return
	}
	worldBookMu.Lock()
	defer worldBookMu.Unlock()

	tree, ok := h.loadWorldBook(c)
	if !ok {
		return
	}
	updated, saved, err := tree.Save(payload.Node, time.Now())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_book.save", err, strings.TrimSpace(payload.Node.Title), nil)
		return
	}
	if err := h.sqlite.SaveWorldBook(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_book.save", err, saved.Title, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_book.save", "世界书节点已保存", saved.Title, map[string]any{"node_id": saved.ID})
	c.JSON(http.StatusOK, gin.H{"node": saved, "nodes": updated.Nodes})
}

// deleteWorldBookNode 删掉一个节点，它的子节点会接到它的父节点上。
func (h *BotHandler) deleteWorldBookNode(c *gin.Context) {
	var payload worldBookDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_book.delete", err, "", nil)
		return
	}
	worldBookMu.Lock()
	defer worldBookMu.Unlock()

	tree, ok := h.loadWorldBook(c)
	if !ok {
		return
	}
	node, _ := tree.Find(payload.ID)
	updated := tree.Delete(payload.ID)
	if err := h.sqlite.SaveWorldBook(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_book.delete", err, node.Title, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_book.delete", "世界书节点已删除", node.Title, map[string]any{"node_id": strings.TrimSpace(payload.ID)})
	c.JSON(http.StatusOK, gin.H{"nodes": updated.Nodes})
}

// importWorldBook 把导出文件并进树里。合并在后端一次完成：一次读改写落一次库，
// 中途失败不会留下「导了一半」的状态。
func (h *BotHandler) importWorldBook(c *gin.Context) {
	var payload worldBookImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_book.import", err, "", nil)
		return
	}
	if len(payload.Nodes) == 0 && len(payload.Entries) > 0 {
		converted, ok := assistant.WorldBookNodesFromSillyTavern(payload.Entries)
		if !ok {
			h.writeError(c, http.StatusBadRequest, "assistant.world_book.import", errWorldBookEntriesInvalid, "", nil)
			return
		}
		payload.Nodes = converted
	}
	if len(payload.Nodes) == 0 {
		h.writeError(c, http.StatusBadRequest, "assistant.world_book.import", errWorldBookImportEmpty, "", nil)
		return
	}
	worldBookMu.Lock()
	defer worldBookMu.Unlock()

	tree, ok := h.loadWorldBook(c)
	if !ok {
		return
	}
	updated, result := tree.Import(payload.Nodes, time.Now())
	if err := h.sqlite.SaveWorldBook(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_book.import", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_book.import", "世界书已导入", "", map[string]any{
		"imported": result.Imported,
		"dropped":  result.Dropped,
	})
	nodes := updated.Nodes
	if nodes == nil {
		nodes = []assistant.WorldBookNode{}
	}
	c.JSON(http.StatusOK, worldBookImportResponse{
		Nodes:    nodes,
		Imported: result.Imported,
		Dropped:  result.Dropped,
	})
}

var (
	errWorldBookStoreUnavailable = errors.New("世界书存储未配置")
	errWorldBookImportEmpty      = errors.New("文件里没有可导入的设定节点")
	errWorldBookEntriesInvalid   = errors.New("无法识别的世界书条目格式")
)
