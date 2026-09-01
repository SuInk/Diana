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

// 世界树（世界观设定库）的控制台接口。树是全局一棵，节点在这里增删改；
// 每台机器人用不用它由各自配置里的 world_tree_enabled 决定。
// 存取模式与人设库一致：整块读、整块写，读改写用互斥锁串行化。

type worldTreeSavePayload struct {
	Node assistant.WorldTreeNode `json:"node"`
}

type worldTreeDeletePayload struct {
	ID string `json:"id"`
}

// worldTreeImportPayload 接的是导出文件的内容。Version 目前只用来在格式变了
// 以后认出旧文件。
type worldTreeImportPayload struct {
	Version int                       `json:"version,omitempty"`
	Nodes   []assistant.WorldTreeNode `json:"nodes"`
}

type worldTreeListResponse struct {
	Nodes []assistant.WorldTreeNode `json:"nodes"`
	Limit int                       `json:"limit"`
}

type worldTreeImportResponse struct {
	Nodes    []assistant.WorldTreeNode `json:"nodes"`
	Imported int                       `json:"imported"`
	Dropped  int                       `json:"dropped"`
}

// worldTreeMu 串行化「读改写」。整块存整块写，两个请求同时保存会让后写的那份
// 把前一份的新增覆盖掉。
var worldTreeMu sync.Mutex

func (h *BotHandler) registerWorldTreeRoutes(router gin.IRouter, base string) {
	router.GET(base+"/world-tree", h.listWorldTree)
	router.POST(base+"/world-tree", h.saveWorldTreeNode)
	router.POST(base+"/world-tree/delete", h.deleteWorldTreeNode)
	router.POST(base+"/world-tree/import", h.importWorldTree)
}

func (h *BotHandler) loadWorldTree(c *gin.Context) (assistant.WorldTree, bool) {
	if h == nil || h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.world_tree", errWorldTreeStoreUnavailable, "", nil)
		return assistant.WorldTree{}, false
	}
	tree, _, err := h.sqlite.LoadWorldTree(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_tree", err, "", nil)
		return assistant.WorldTree{}, false
	}
	return tree.WithDefaults(), true
}

// listWorldTree 返回整棵树。
func (h *BotHandler) listWorldTree(c *gin.Context) {
	tree, ok := h.loadWorldTree(c)
	if !ok {
		return
	}
	nodes := tree.Nodes
	if nodes == nil {
		nodes = []assistant.WorldTreeNode{}
	}
	c.JSON(http.StatusOK, worldTreeListResponse{Nodes: nodes, Limit: assistant.WorldTreeMaxNodes})
}

// saveWorldTreeNode 新增或更新一个节点。带 ID 是改，不带是新增。
func (h *BotHandler) saveWorldTreeNode(c *gin.Context) {
	var payload worldTreeSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_tree.save", err, "", nil)
		return
	}
	worldTreeMu.Lock()
	defer worldTreeMu.Unlock()

	tree, ok := h.loadWorldTree(c)
	if !ok {
		return
	}
	updated, saved, err := tree.Save(payload.Node, time.Now())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_tree.save", err, strings.TrimSpace(payload.Node.Title), nil)
		return
	}
	if err := h.sqlite.SaveWorldTree(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_tree.save", err, saved.Title, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_tree.save", "世界树节点已保存", saved.Title, map[string]any{"node_id": saved.ID})
	c.JSON(http.StatusOK, gin.H{"node": saved, "nodes": updated.Nodes})
}

// deleteWorldTreeNode 删掉一个节点，它的子节点会接到它的父节点上。
func (h *BotHandler) deleteWorldTreeNode(c *gin.Context) {
	var payload worldTreeDeletePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_tree.delete", err, "", nil)
		return
	}
	worldTreeMu.Lock()
	defer worldTreeMu.Unlock()

	tree, ok := h.loadWorldTree(c)
	if !ok {
		return
	}
	node, _ := tree.Find(payload.ID)
	updated := tree.Delete(payload.ID)
	if err := h.sqlite.SaveWorldTree(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_tree.delete", err, node.Title, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_tree.delete", "世界树节点已删除", node.Title, map[string]any{"node_id": strings.TrimSpace(payload.ID)})
	c.JSON(http.StatusOK, gin.H{"nodes": updated.Nodes})
}

// importWorldTree 把导出文件并进树里。合并在后端一次完成：一次读改写落一次库，
// 中途失败不会留下「导了一半」的状态。
func (h *BotHandler) importWorldTree(c *gin.Context) {
	var payload worldTreeImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.world_tree.import", err, "", nil)
		return
	}
	if len(payload.Nodes) == 0 {
		h.writeError(c, http.StatusBadRequest, "assistant.world_tree.import", errWorldTreeImportEmpty, "", nil)
		return
	}
	worldTreeMu.Lock()
	defer worldTreeMu.Unlock()

	tree, ok := h.loadWorldTree(c)
	if !ok {
		return
	}
	updated, result := tree.Import(payload.Nodes, time.Now())
	if err := h.sqlite.SaveWorldTree(c.Request.Context(), updated); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.world_tree.import", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.world_tree.import", "世界树已导入", "", map[string]any{
		"imported": result.Imported,
		"dropped":  result.Dropped,
	})
	nodes := updated.Nodes
	if nodes == nil {
		nodes = []assistant.WorldTreeNode{}
	}
	c.JSON(http.StatusOK, worldTreeImportResponse{
		Nodes:    nodes,
		Imported: result.Imported,
		Dropped:  result.Dropped,
	})
}

var (
	errWorldTreeStoreUnavailable = errors.New("世界树存储未配置")
	errWorldTreeImportEmpty      = errors.New("文件里没有可导入的设定节点")
)
