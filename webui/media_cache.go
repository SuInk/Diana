// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type MediaCachePolicyStore interface {
	LoadMediaDownloadCachePolicy(context.Context) (assistant.MediaDownloadCachePolicy, bool, error)
	SaveMediaDownloadCachePolicy(context.Context, assistant.MediaDownloadCachePolicy) error
}

type MediaCacheHandler struct {
	mu     sync.Mutex
	store  MediaCachePolicyStore
	policy assistant.MediaDownloadCachePolicy
	logs   AppLogWriter
}

func NewMediaCacheHandler(ctx context.Context, store MediaCachePolicyStore, fallback assistant.MediaDownloadCachePolicy) (*MediaCacheHandler, error) {
	policy, found, err := store.LoadMediaDownloadCachePolicy(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		policy = fallback
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	policy = policy.WithDefaults()
	if err := assistant.ConfigureMediaDownloadCache(policy.RetentionDays, policy.MaxMB<<20); err != nil {
		return nil, err
	}
	return &MediaCacheHandler{store: store, policy: policy}, nil
}

func (h *MediaCacheHandler) SetLogStore(logs AppLogWriter) { h.logs = logs }

func (h *MediaCacheHandler) Register(router gin.IRouter) {
	router.GET("/api/system/media-cache", h.get)
	router.POST("/api/system/media-cache", h.save)
}

func (h *MediaCacheHandler) get(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.JSON(http.StatusOK, h.policy)
}

func (h *MediaCacheHandler) save(c *gin.Context) {
	var payload struct {
		RetentionDays *int   `json:"retention_days"`
		MaxMB         *int64 `json:"max_mb"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if payload.RetentionDays == nil || payload.MaxMB == nil {
		writeError(c, http.StatusBadRequest, fmt.Errorf("缺少缓存保留天数或容量设置"))
		return
	}
	policy := assistant.MediaDownloadCachePolicy{RetentionDays: *payload.RetentionDays, MaxMB: *payload.MaxMB}
	if err := policy.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	policy = policy.WithDefaults()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.store.SaveMediaDownloadCachePolicy(c.Request.Context(), policy); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	// Validation precedes persistence; applying this exact policy cannot fail.
	if err := assistant.ConfigureMediaDownloadCache(policy.RetentionDays, policy.MaxMB<<20); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	h.policy = policy
	if err := assistant.CleanupMediaDownloadCache(); err != nil {
		log.Printf("download cache policy applied; cleanup deferred: %v", err)
	}
	recordRequestOperation(c, h.logs, "system.media-cache.save", "下载缓存策略已更新", "", map[string]any{"retention_days": policy.RetentionDays, "max_mb": policy.MaxMB})
	c.JSON(http.StatusOK, policy)
}
