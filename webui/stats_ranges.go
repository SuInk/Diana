// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// 文件名不叫 llm_usage_windows.go：以 _windows 结尾的文件会被 Go 当成 Windows
// 专属实现，在 macOS 和 Linux 上整个不参与编译。

// statsRangeDurations 是总览页能切的几个时间窗，顺序即展示顺序。
var statsRangeDurations = []struct {
	ID       string
	Duration time.Duration
}{
	{ID: "1h", Duration: time.Hour},
	{ID: "12h", Duration: 12 * time.Hour},
	{ID: "24h", Duration: 24 * time.Hour},
}

// eventStatsRangeReader 是时间窗统计对存储的最小依赖，便于测试注入。
type eventStatsRangeReader interface {
	EventStatsRange(ctx context.Context, since, until time.Time) (storage.EventRangeStats, error)
}

// StatsRange 是一个时间窗里总览页要用的全部数字。
type StatsRange struct {
	ID string `json:"id"`
	storage.EventRangeStats
	Usage applog.UsageSummary `json:"usage"`
}

// StatsRangesResponse 是 GET /api/stats/ranges 的响应。
//
// 三个窗口一次全给：它们互相包含，用户来回切换时不该每切一次就再打一趟后端。
type StatsRangesResponse struct {
	Until  time.Time    `json:"until"`
	Ranges []StatsRange `json:"ranges"`
}

// statsRanges 汇总最近 1 / 12 / 24 小时的消息处理情况和模型用量。
//
// 数字取自库里的队列事件和用量日志，而不是进程内那几个累加器：累加器一重启就清零，
// 而「最近 24 小时」恰恰要跨重启才成立。
func (h *StatsHandler) statsRanges(c *gin.Context) {
	if h.usage == nil || h.events == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "统计存储不可用"})
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	ctx := c.Request.Context()
	response := StatsRangesResponse{Until: now.UTC(), Ranges: make([]StatsRange, 0, len(statsRangeDurations))}
	for _, window := range statsRangeDurations {
		since := now.Add(-window.Duration)
		events, err := h.events.EventStatsRange(ctx, since, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取消息统计失败：" + err.Error()})
			return
		}
		usage, err := h.usage.LLMUsageSince(ctx, since, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取用量统计失败：" + err.Error()})
			return
		}
		response.Ranges = append(response.Ranges, StatsRange{ID: window.ID, EventRangeStats: events, Usage: usage})
	}
	c.JSON(http.StatusOK, response)
}
