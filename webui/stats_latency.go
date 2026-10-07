// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// replyLatencyReader 是响应耗时明细对存储的最小依赖，便于测试注入。
type replyLatencyReader interface {
	ReplyLatencySamples(ctx context.Context, since, until time.Time, profileID string) ([]storage.ReplyLatencySample, error)
}

// latencyWindows 是响应耗时明细里能切的几个窗口。每个窗口都和紧挨着的前一段等长
// 窗口比，所以一次要读两倍长度的样本。
var latencyWindows = []struct {
	ID       string
	Duration time.Duration
}{
	{ID: "1h", Duration: time.Hour},
	{ID: "24h", Duration: 24 * time.Hour},
	{ID: "7d", Duration: 7 * 24 * time.Hour},
}

// latencyExtremes 是每个窗口列出的最慢、最快回复条数。
const latencyExtremes = 5

// LatencyWindow 是一个窗口的耗时分布，以及前一段等长窗口的分布，用来看变快还是变慢。
type LatencyWindow struct {
	ID       string                      `json:"id"`
	Current  storage.ReplyLatencySummary `json:"current"`
	Previous storage.ReplyLatencySummary `json:"previous"`
}

// LatencyResponse 是 GET /api/stats/latency 的响应。
type LatencyResponse struct {
	Until   time.Time       `json:"until"`
	Windows []LatencyWindow `json:"windows"`
}

// WithLatencyReader 注入响应耗时明细要用的读取器。
func (h *StatsHandler) WithLatencyReader(reader replyLatencyReader) *StatsHandler {
	h.latency = reader
	return h
}

// statsLatency 返回最近 1 小时 / 24 小时 / 7 天的回复耗时分位数和阶段分解。
//
// 和 /api/stats/ranges 一样读库而不读进程内累加器：7 天窗口必须跨重启才成立。
// profile_id 给了就只看这台机器人。window 给了就只算这一档：读样本要把这段时间的
// 模型调用日志逐条对到回复上，7 天档连同对比的前 7 天要读两周，而界面默认看的
// 24 小时只需要两天，一次全算会让弹窗白等。
func (h *StatsHandler) statsLatency(c *gin.Context) {
	if h.latency == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "统计存储不可用"})
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	windows := latencyWindows
	if id := strings.TrimSpace(c.Query("window")); id != "" {
		windows = nil
		for _, window := range latencyWindows {
			if window.ID == id {
				windows = append(windows, window)
			}
		}
		if len(windows) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知的统计窗口：" + id})
			return
		}
	}
	longest := windows[len(windows)-1].Duration
	samples, err := h.latency.ReplyLatencySamples(c.Request.Context(), now.Add(-2*longest), now, strings.TrimSpace(c.Query("profile_id")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取响应耗时失败：" + err.Error()})
		return
	}
	response := LatencyResponse{Until: now.UTC(), Windows: make([]LatencyWindow, 0, len(windows))}
	for _, window := range windows {
		since := now.Add(-window.Duration)
		response.Windows = append(response.Windows, LatencyWindow{
			ID:       window.ID,
			Current:  storage.SummarizeReplyLatency(samples, since, now, latencyExtremes),
			Previous: storage.SummarizeReplyLatency(samples, since.Add(-window.Duration), since, 0),
		})
	}
	c.JSON(http.StatusOK, response)
}
