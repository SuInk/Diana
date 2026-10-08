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

// inboundActivityReader 是活跃时段热力图对存储的最小依赖，便于测试注入。
type inboundActivityReader interface {
	InboundActivityBuckets(ctx context.Context, since, until time.Time, profileID, groupID string) ([]storage.InboundActivityBucket, error)
	InboundActivityDays(ctx context.Context, since, until time.Time, utcOffsetSeconds int, profileID, groupID string) ([]storage.InboundActivityDayCount, error)
}

// activityWindows 是「星期 × 钟点」热力图能切的窗口。都是整周：滚动的整周窗口里，
// 每个「星期几 × 几点」恰好出现同样多次，格子之间才能直接比、才能除出周均。
var activityWindows = []struct {
	ID    string
	Weeks int
}{
	{ID: "7d", Weeks: 1},
	{ID: "28d", Weeks: 4},
}

// activityCalendarDays 是按天日历往回看的天数：53 周，和 GitHub 贡献图一样是近一年。
const activityCalendarDays = 53 * 7

// ActivityWindow 是一个窗口里按星期和小时汇总的收到消息数。
type ActivityWindow struct {
	ID    string    `json:"id"`
	Weeks int       `json:"weeks"`
	Since time.Time `json:"since"`
	Total int64     `json:"total"`
	// Cells[星期][小时]，星期从周一（0）排到周日（6），小时是服务器本地时间。
	Cells [7][24]int64 `json:"cells"`
	// FirstEventAt 是窗口里最早一条消息所在的 15 分钟；没有消息时为空。比 Since 晚出
	// 一大截说明实例装上还不满这个窗口，前端据此提示周均会偏低。
	FirstEventAt *time.Time `json:"first_event_at,omitempty"`
}

// ActivityDay 是服务器本地时间某一天收到的消息数。
type ActivityDay struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// ActivityResponse 是 GET /api/stats/activity 的响应。
type ActivityResponse struct {
	Until time.Time `json:"until"`
	// Today 是服务器本地的今天（YYYY-MM-DD），日历从这一天往回画。
	Today string `json:"today"`
	// UTCOffset 是钟点和日期所用的服务器时区，形如 +08:00：浏览器和服务器不在一个
	// 时区时，前端要把这一点说出来，不然「晚上 9 点最热闹」会被读成浏览器所在地的 9 点。
	UTCOffset string           `json:"utc_offset"`
	Windows   []ActivityWindow `json:"windows"`
	// Days 只列有消息的日子，按日期升序，覆盖近一年；没列出的过去日子就是没有消息。
	Days []ActivityDay `json:"days"`
}

// WithActivityReader 注入活跃时段热力图要用的读取器。
func (h *StatsHandler) WithActivityReader(reader inboundActivityReader) *StatsHandler {
	h.activity = reader
	return h
}

// statsActivity 返回收到的消息按「星期几 × 几点」的分布（最近 7 天 / 28 天），
// 以及近一年按天的消息数。
//
// 和响应耗时一样读库：这些窗口都必须跨重启才成立。只查一次最长的范围，其余从同一批
// 分桶里切出来。profile_id 给了就只看这台机器人，group_id 给了就只看这个群。
func (h *StatsHandler) statsActivity(c *gin.Context) {
	if h.activity == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "统计存储不可用"})
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	profileID, groupID := strings.TrimSpace(c.Query("profile_id")), strings.TrimSpace(c.Query("group_id"))
	ctx := c.Request.Context()
	longest := time.Duration(activityWindows[len(activityWindows)-1].Weeks) * 7 * 24 * time.Hour
	buckets, err := h.activity.InboundActivityBuckets(ctx, now.Add(-longest), now, profileID, groupID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取活跃时段失败：" + err.Error()})
		return
	}
	_, offsetSeconds := now.Zone()
	days, err := h.activity.InboundActivityDays(ctx, today.AddDate(0, 0, -(activityCalendarDays-1)), now, offsetSeconds, profileID, groupID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取每日消息量失败：" + err.Error()})
		return
	}
	response := ActivityResponse{
		Until:     now.UTC(),
		Today:     today.Format(time.DateOnly),
		UTCOffset: now.Format("-07:00"),
		Windows:   make([]ActivityWindow, 0, len(activityWindows)),
	}
	for _, window := range activityWindows {
		since := now.Add(-time.Duration(window.Weeks) * 7 * 24 * time.Hour)
		response.Windows = append(response.Windows, summarizeActivity(window.ID, window.Weeks, since, buckets, location))
	}
	response.Days = make([]ActivityDay, 0, len(days))
	for _, day := range days {
		response.Days = append(response.Days, ActivityDay{Date: day.Date, Count: day.Count})
	}
	c.JSON(http.StatusOK, response)
}

// summarizeActivity 把起点不早于 since 的分桶按本地时间的星期和小时累加起来。
// 窗口起点所在的那个桶只取了一部分消息，按起点归属会被整个剔掉——最多少算 15 分钟，
// 换来的是短窗口不会混进窗口外的消息。
func summarizeActivity(id string, weeks int, since time.Time, buckets []storage.InboundActivityBucket, location *time.Location) ActivityWindow {
	window := ActivityWindow{ID: id, Weeks: weeks, Since: since.UTC()}
	for _, bucket := range buckets {
		if bucket.Start.Before(since) {
			continue
		}
		local := bucket.Start.In(location)
		weekday := (int(local.Weekday()) + 6) % 7
		window.Cells[weekday][local.Hour()] += bucket.Count
		window.Total += bucket.Count
		if window.FirstEventAt == nil {
			first := bucket.Start
			window.FirstEventAt = &first
		}
	}
	return window
}
