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

type stubLatencyReader struct {
	samples   []storage.ReplyLatencySample
	since     time.Time
	profileID string
}

func (s *stubLatencyReader) ReplyLatencySamples(_ context.Context, since, _ time.Time, profileID string) ([]storage.ReplyLatencySample, error) {
	s.since, s.profileID = since, profileID
	return s.samples, nil
}

func newLatencyRouter(reader replyLatencyReader, now time.Time) *gin.Engine {
	handler := NewStatsHandler(NewStatsCollector(), &fakeStatusProvider{})
	if reader != nil {
		handler.WithLatencyReader(reader)
	}
	handler.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

func TestStatsLatencyWindowsAndPreviousPeriod(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	sample := func(id string, ago time.Duration, total int64) storage.ReplyLatencySample {
		return storage.ReplyLatencySample{EventID: id, CompletedAt: now.Add(-ago), TotalMS: total}
	}
	reader := &stubLatencyReader{samples: []storage.ReplyLatencySample{
		sample("recent", 10*time.Minute, 2000),
		sample("prev-hour", 90*time.Minute, 8000),
		sample("yesterday", 30*time.Hour, 4000),
		sample("last-week", 10*24*time.Hour, 6000),
	}}
	router := newLatencyRouter(reader, now)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/latency?profile_id=bot-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response LatencyResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	// 一次读够最长窗口和它前一段：14 天。
	if want := now.Add(-14 * 24 * time.Hour); !reader.since.Equal(want) || reader.profileID != "bot-a" {
		t.Fatalf("reader asked since %s for %q", reader.since, reader.profileID)
	}
	if len(response.Windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(response.Windows))
	}
	want := []struct {
		id                string
		current, previous int
	}{
		{"1h", 1, 1},
		{"24h", 2, 1},
		{"7d", 3, 1},
	}
	for index, window := range response.Windows {
		if window.ID != want[index].id || window.Current.Total.Samples != want[index].current || window.Previous.Total.Samples != want[index].previous {
			t.Fatalf("window[%d] = %s current %d previous %d, want %+v", index, window.ID,
				window.Current.Total.Samples, window.Previous.Total.Samples, want[index])
		}
		// 前一段只用来对比，不带样本列表。
		if len(window.Previous.Slowest) != 0 {
			t.Fatalf("window[%d] previous lists extremes", index)
		}
	}
	if slowest := response.Windows[1].Current.Slowest; len(slowest) == 0 || slowest[0].EventID != "prev-hour" {
		t.Fatalf("24h slowest = %+v", slowest)
	}
}

func TestStatsLatencyWithoutReader(t *testing.T) {
	router := newLatencyRouter(nil, time.Now())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/latency", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestStatsLatencySingleWindowReadsOnlyItsSpan(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	reader := &stubLatencyReader{samples: []storage.ReplyLatencySample{
		{EventID: "recent", CompletedAt: now.Add(-10 * time.Minute), TotalMS: 2000},
		{EventID: "yesterday", CompletedAt: now.Add(-30 * time.Hour), TotalMS: 4000},
	}}
	router := newLatencyRouter(reader, now)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/latency?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response LatencyResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	// 只读 24 小时和它前一段：48 小时，而不是 7 天档要的两周。
	if want := now.Add(-48 * time.Hour); !reader.since.Equal(want) {
		t.Fatalf("reader asked since %s, want %s", reader.since, want)
	}
	if len(response.Windows) != 1 || response.Windows[0].ID != "24h" {
		t.Fatalf("windows = %+v", response.Windows)
	}
	if response.Windows[0].Current.Total.Samples != 1 || response.Windows[0].Previous.Total.Samples != 1 {
		t.Fatalf("24h current %d previous %d", response.Windows[0].Current.Total.Samples, response.Windows[0].Previous.Total.Samples)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/latency?window=30d", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown window status = %d, want 400", rec.Code)
	}
}
