// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type MediaBaseURLStore interface {
	LoadLocalMediaBaseURL(context.Context) (string, bool, error)
	SaveLocalMediaBaseURL(context.Context, string) error
}

// MediaBaseURLHandler 把媒体回源基址暴露成 WebUI 可读写的一项设置，持久化在
// 数据库，保存即热生效（不需要重启）。留空表示自动推断；没保存过时值回落到
// config.yaml 的 storage.local_media_base_url，再没有才走推断链。
type MediaBaseURLHandler struct {
	mu             sync.Mutex
	store          MediaBaseURLStore
	configFallback string
	baseURL        string
	source         string
	apply          func(string)
	logs           AppLogWriter
}

// NewMediaBaseURLHandler 从数据库恢复已保存的值；没有就用 configFallback
// （config.yaml 的 storage.local_media_base_url）。apply 把当前生效值推给
// LocalMediaStore，保存后立即调用，进程重启后由这里恢复。
func NewMediaBaseURLHandler(ctx context.Context, store MediaBaseURLStore, configFallback string, apply func(string)) (*MediaBaseURLHandler, error) {
	baseURL, _, err := store.LoadLocalMediaBaseURL(ctx)
	if err != nil {
		return nil, err
	}
	h := &MediaBaseURLHandler{store: store, configFallback: strings.TrimSpace(configFallback), apply: apply}
	switch {
	case strings.TrimSpace(baseURL) != "":
		h.baseURL, h.source = strings.TrimRight(strings.TrimSpace(baseURL), "/"), "database"
	case h.configFallback != "":
		h.baseURL, h.source = h.configFallback, "config"
	default:
		h.source = "auto"
	}
	if apply != nil {
		apply(h.baseURL)
	}
	return h, nil
}

func (h *MediaBaseURLHandler) SetLogStore(logs AppLogWriter) { h.logs = logs }

func (h *MediaBaseURLHandler) Register(router gin.IRouter) {
	router.GET("/api/system/media-base-url", h.get)
	router.POST("/api/system/media-base-url", h.save)
}

func (h *MediaBaseURLHandler) get(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"base_url": h.baseURL, "source": h.source})
}

func (h *MediaBaseURLHandler) save(c *gin.Context) {
	var payload struct {
		BaseURL string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(payload.BaseURL), "/")
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeError(c, http.StatusBadRequest, fmt.Errorf("媒体回源基址必须是 http(s) URL，例如 http://192.168.1.10:18080/media/resolver"))
			return
		}
		if parsed.Path != "" && parsed.Path != "/media/resolver" {
			writeError(c, http.StatusBadRequest, fmt.Errorf("媒体回源基址的路径必须是 /media/resolver 或留空"))
			return
		}
		baseURL = parsed.Scheme + "://" + parsed.Host + "/media/resolver"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.store.SaveLocalMediaBaseURL(c.Request.Context(), baseURL); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	h.baseURL = baseURL
	switch {
	case baseURL != "":
		h.source = "database"
	case h.configFallback != "":
		h.baseURL, h.source = h.configFallback, "config"
	default:
		h.source = "auto"
	}
	if h.apply != nil {
		h.apply(h.baseURL)
	}
	recordRequestOperation(c, h.logs, "system.media-base-url.save", "媒体回源基址已更新", "", map[string]any{"base_url": h.baseURL, "source": h.source})
	c.JSON(http.StatusOK, gin.H{"base_url": h.baseURL, "source": h.source})
}
