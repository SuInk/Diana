// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// stickerLibraryQueryFrom 从查询参数读出表情包池的筛选条件，列表、分类栏和清理共用。
func stickerLibraryQueryFrom(c *gin.Context) storage.StickerLibraryQuery {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	idleDays, _ := strconv.Atoi(c.Query("idle_days"))
	return storage.StickerLibraryQuery{
		ProfileID: botProfileScope(c),
		Search:    strings.TrimSpace(c.Query("q")),
		Category:  strings.TrimSpace(c.Query("category")),
		Tag:       strings.TrimSpace(c.Query("tag")),
		Source:    strings.TrimSpace(c.Query("source")),
		IdleDays:  max(idleDays, 0),
		NeverSent: c.Query("never_sent") == "true",
		Sort:      strings.TrimSpace(c.Query("sort")),
		Limit:     limit,
		Offset:    offset,
	}
}

// listStickers 列出表情包池里已经收录的表情包，供插件设置页浏览。
func (h *BotHandler) listStickers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	page, err := h.sqlite.ListStickerLibrary(c.Request.Context(), stickerLibraryQueryFrom(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取表情包池失败"})
		return
	}
	c.JSON(http.StatusOK, page)
}

// stickerFacets 返回表情包池的分类栏：画风大类、来源会话和常见关键词，按当前筛选条件计数。
func (h *BotHandler) stickerFacets(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	facets, err := h.sqlite.ListStickerLibraryFacets(c.Request.Context(), stickerLibraryQueryFrom(c), 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取表情包分类失败"})
		return
	}
	c.JSON(http.StatusOK, facets)
}

// cleanupStickers 按筛选条件批量移出表情包池。dry_run=true 只返回会删多少，控制台先拿它做确认。
func (h *BotHandler) cleanupStickers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	result, err := h.sqlite.CleanupStickerLibrary(c.Request.Context(), stickerLibraryQueryFrom(c), c.Query("block") == "true", c.Query("dry_run") == "true")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清理表情包失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// deleteSticker 把一张表情包移出池子，之后再收到同一张也不收录。
func (h *BotHandler) deleteSticker(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	removed, err := h.sqlite.DeleteStickerAsset(c.Request.Context(), c.Param("hash"), botProfileScope(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除表情包失败"})
		return
	}
	if removed == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "表情包不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}

// stickerImage 按图片哈希返回表情包本体；本地路径只在服务端用，不下发。
func (h *BotHandler) stickerImage(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	path, found, err := h.sqlite.StickerAssetFile(c.Request.Context(), c.Param("hash"), botProfileScope(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取表情包失败"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "表情包不存在"})
		return
	}
	h.writeEventImage(c, assistant.MessageSegment{Type: "image", Data: map[string]string{"cached_file": path}})
}
