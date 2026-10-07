// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type stubActivityReader struct {
	buckets   []storage.InboundActivityBucket
	days      []storage.InboundActivityDayCount
	since     time.Time
	daysSince time.Time
	offset    int
	profileID string
	groupID   string
}

func (s *stubActivityReader) InboundActivityDays(_ context.Context, since, _ time.Time, offset int, _, _ string) ([]storage.InboundActivityDayCount, error) {
	s.daysSince, s.offset = since, offset
	return s.days, nil
}

func (s *stubActivityReader) InboundActivityBuckets(_ context.Context, since, _ time.Time, profileID, groupID string) ([]storage.InboundActivityBucket, error) {
	s.since, s.profileID, s.groupID = since, profileID, groupID
	return s.buckets, nil
}

func newActivityRouter(reader inboundActivityReader, now time.Time) *gin.Engine {
	handler := NewStatsHandler(NewStatsCollector(), &fakeStatusProvider{})
	if reader != nil {
		handler.WithActivityReader(reader)
	}
	handler.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

func TestStatsActivityBucketsByLocalWeekdayHourAndDay(t *testing.T) {
	shanghai := time.FixedZone("UTC+8", 8*3600)
	// 2026-10-07 是周三。
	now := time.Date(2026, 10, 7, 22, 0, 0, 0, shanghai)
	bucket := func(at time.Time, count int64) storage.InboundActivityBucket {
		return storage.InboundActivityBucket{Start: at.UTC(), Count: count}
	}
	reader := &stubActivityReader{days: []storage.InboundActivityDayCount{{Date: "2026-08-03", Count: 6}, {Date: "2026-10-07", Count: 8}}, buckets: []storage.InboundActivityBucket{
		// 三周前的周一 9:15（本地），进 28 天窗口不进 7 天窗口。
		bucket(time.Date(2026, 9, 14, 9, 15, 0, 0, shanghai), 4),
		// 周日 23:45（本地），UTC 下已经是周日 15:45，要按本地算成周日 23 点。
		bucket(time.Date(2026, 10, 4, 23, 45, 0, 0, shanghai), 2),
		// 今天 21:00 和 21:30 两个桶合进同一格、同一天。
		bucket(time.Date(2026, 10, 7, 21, 0, 0, 0, shanghai), 3),
		bucket(time.Date(2026, 10, 7, 21, 30, 0, 0, shanghai), 5),
	}}
	router := newActivityRouter(reader, now)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/activity?profile_id=bot-a&group_id=10001", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response ActivityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if reader.profileID != "bot-a" || reader.groupID != "10001" {
		t.Fatalf("filters = %q / %q", reader.profileID, reader.groupID)
	}
	if want := now.Add(-28 * 24 * time.Hour); !reader.since.Equal(want) {
		t.Fatalf("buckets since = %v, want %v", reader.since, want)
	}
	if want := time.Date(2026, 10, 7, 0, 0, 0, 0, shanghai).AddDate(0, 0, -(activityCalendarDays - 1)); !reader.daysSince.Equal(want) || reader.offset != 8*3600 {
		t.Fatalf("days since = %v offset = %d, want %v / 28800", reader.daysSince, reader.offset, want)
	}
	if response.UTCOffset != "+08:00" || response.Today != "2026-10-07" {
		t.Fatalf("utc_offset = %q, today = %q", response.UTCOffset, response.Today)
	}
	if len(response.Windows) != 2 || response.Windows[0].ID != "7d" || response.Windows[1].ID != "28d" {
		t.Fatalf("windows = %+v", response.Windows)
	}

	week, month := response.Windows[0], response.Windows[1]
	if week.Total != 10 || month.Total != 14 {
		t.Fatalf("totals = %d / %d, want 10 / 14", week.Total, month.Total)
	}
	if week.Cells[0][9] != 0 || month.Cells[0][9] != 4 {
		t.Fatalf("monday 9h = %d / %d, want 0 / 4", week.Cells[0][9], month.Cells[0][9])
	}
	if week.Cells[6][23] != 2 || week.Cells[2][21] != 8 {
		t.Fatalf("sunday 23h = %d, wednesday 21h = %d", week.Cells[6][23], week.Cells[2][21])
	}
	if month.FirstEventAt == nil || !month.FirstEventAt.Equal(time.Date(2026, 9, 14, 9, 15, 0, 0, shanghai)) {
		t.Fatalf("first_event_at = %v", month.FirstEventAt)
	}

	wantDays := []ActivityDay{{"2026-08-03", 6}, {"2026-10-07", 8}}
	if len(response.Days) != len(wantDays) {
		t.Fatalf("days = %+v", response.Days)
	}
	for index, day := range wantDays {
		if response.Days[index] != day {
			t.Fatalf("days[%d] = %+v, want %+v", index, response.Days[index], day)
		}
	}
}

func TestStatsActivityWithoutStorageIsUnavailable(t *testing.T) {
	router := newActivityRouter(nil, time.Now())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/activity", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
