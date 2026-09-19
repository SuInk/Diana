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

// listStickers 列出表情包池里已经收录的表情包，供插件设置页浏览。
func (h *BotHandler) listStickers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	page, err := h.sqlite.ListStickerLibrary(c.Request.Context(), storage.StickerLibraryQuery{
		ProfileID: botProfileScope(c),
		Search:    strings.TrimSpace(c.Query("q")),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取表情包池失败"})
		return
	}
	c.JSON(http.StatusOK, page)
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
