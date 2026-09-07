// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type HistoryMediaPolicyStore interface {
	LoadHistoryMediaRetentionPolicy(context.Context) (assistant.HistoryMediaRetentionPolicy, bool, error)
	SaveHistoryMediaRetentionPolicy(context.Context, assistant.HistoryMediaRetentionPolicy) error
}

type HistoryMediaHandler struct {
	mu     sync.Mutex
	store  HistoryMediaPolicyStore
	policy assistant.HistoryMediaRetentionPolicy
	logs   AppLogWriter
}

func NewHistoryMediaHandler(ctx context.Context, store HistoryMediaPolicyStore, fallback assistant.HistoryMediaRetentionPolicy) (*HistoryMediaHandler, error) {
	policy, found, err := store.LoadHistoryMediaRetentionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		policy = fallback
	}
	if err := ConfigureHistoryMediaPolicy(policy); err != nil {
		return nil, err
	}
	return &HistoryMediaHandler{store: store, policy: policy.WithDefaults()}, nil
}

func ConfigureHistoryMediaPolicy(policy assistant.HistoryMediaRetentionPolicy) error {
	return assistant.ConfigureHistoryMediaRetention(policy)
}
func (h *HistoryMediaHandler) SetLogStore(logs AppLogWriter) { h.logs = logs }
func (h *HistoryMediaHandler) Register(router gin.IRouter) {
	router.GET("/api/system/history-media", h.get)
	router.POST("/api/system/history-media", h.save)
}
func (h *HistoryMediaHandler) get(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.JSON(http.StatusOK, h.policy)
}
func (h *HistoryMediaHandler) save(c *gin.Context) {
	var payload struct {
		RetentionDays *int   `json:"retention_days"`
		MaxMB         *int64 `json:"max_mb"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if payload.RetentionDays == nil || payload.MaxMB == nil {
		writeError(c, http.StatusBadRequest, fmt.Errorf("缺少历史媒体保留天数或容量设置"))
		return
	}
	policy := assistant.HistoryMediaRetentionPolicy{RetentionDays: *payload.RetentionDays, MaxMB: *payload.MaxMB}.WithDefaults()
	if err := policy.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.store.SaveHistoryMediaRetentionPolicy(c.Request.Context(), policy); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	_ = assistant.ConfigureHistoryMediaRetention(policy)
	result, err := assistant.CleanupHistoryMedia()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	h.policy = policy
	recordRequestOperation(c, h.logs, "system.history-media.save", "历史媒体策略已更新", "", map[string]any{"retention_days": policy.RetentionDays, "max_mb": policy.MaxMB, "deleted_files": result.DeletedFiles, "deleted_bytes": result.DeletedBytes})
	c.JSON(http.StatusOK, policy)
}
