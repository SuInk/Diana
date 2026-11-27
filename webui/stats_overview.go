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

type dashboardOverviewReader interface {
	DashboardOverview(ctx context.Context, query storage.DashboardOverviewQuery) (storage.DashboardOverviewData, error)
}

// StatsOverviewResponse 是 GET /api/stats/overview 的响应结构。
type StatsOverviewResponse struct {
	Range     string `json:"range"`
	UTCOffset string `json:"utc_offset"`
	storage.DashboardOverviewData
}

func (h *StatsHandler) statsOverview(c *gin.Context) {
	if h.overview == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "统计存储不可用"})
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	rangeID := strings.TrimSpace(c.DefaultQuery("range", "today"))
	since, until, ok := resolveStatsOverviewBounds(rangeID, c.Query("since"), c.Query("until"), now)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的统计时间范围"})
		return
	}
	ctx := c.Request.Context()
	data, err := h.overview.DashboardOverview(ctx, storage.DashboardOverviewQuery{
		Since:      since,
		Until:      until,
		ProfileID:  strings.TrimSpace(c.Query("profile_id")),
		ErrorLimit: 20,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取综合统计失败：" + err.Error()})
		return
	}
	if h.groupNames != nil && (len(data.Groups) > 0 || len(data.Errors.Recent) > 0) {
		names := h.groupNames(ctx)
		for i := range data.Groups {
			if name := strings.TrimSpace(names[data.Groups[i].GroupID]); name != "" {
				data.Groups[i].GroupName = name
			}
		}
		for i := range data.Errors.Recent {
			if gid := data.Errors.Recent[i].GroupID; gid != "" {
				if name := strings.TrimSpace(names[gid]); name != "" {
					data.Errors.Recent[i].GroupName = name
				}
			}
		}
	}
	c.JSON(http.StatusOK, StatsOverviewResponse{
		Range:                 rangeID,
		UTCOffset:             now.Format("-07:00"),
		DashboardOverviewData: data,
	})
}

func resolveStatsOverviewBounds(rangeID, rawSince, rawUntil string, now time.Time) (time.Time, time.Time, bool) {
	rawSince = strings.TrimSpace(rawSince)
	rawUntil = strings.TrimSpace(rawUntil)
	if rawSince != "" || rawUntil != "" || rangeID == "custom" {
		var since, until time.Time
		var err error
		if rawSince != "" {
			since, err = time.Parse(time.RFC3339Nano, rawSince)
			if err != nil {
				return time.Time{}, time.Time{}, false
			}
		}
		if rawUntil != "" {
			until, err = time.Parse(time.RFC3339Nano, rawUntil)
			if err != nil {
				return time.Time{}, time.Time{}, false
			}
		} else {
			until = now
		}
		if since.IsZero() {
			since = until.AddDate(0, 0, -30)
		}
		if !since.Before(until) {
			return time.Time{}, time.Time{}, false
		}
		return since, until, true
	}

	loc := now.Location()
	switch rangeID {
	case "today":
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		if !dayStart.Before(now) {
			return dayStart, dayStart.Add(time.Second), true
		}
		return dayStart, now, true
	case "1h":
		return now.Add(-time.Hour), now, true
	case "12h":
		return now.Add(-12 * time.Hour), now, true
	case "24h":
		return now.Add(-24 * time.Hour), now, true
	case "7d":
		return now.AddDate(0, 0, -7), now, true
	case "30d":
		return now.AddDate(0, 0, -30), now, true
	case "90d":
		return now.AddDate(0, 0, -90), now, true
	case "365d":
		return now.AddDate(-1, 0, 0), now, true
	default:
		return time.Time{}, time.Time{}, false
	}
}
