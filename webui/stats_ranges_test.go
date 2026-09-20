// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// stubRangeReader 按窗口长度回账，好让断言直接看出每个窗口问的是哪一段。
type stubRangeReader struct {
	calls []time.Duration
	err   error
}

func (s *stubRangeReader) EventStatsRange(_ context.Context, since, until time.Time) (storage.EventRangeStats, error) {
	if s.err != nil {
		return storage.EventRangeStats{}, s.err
	}
	hours := int64(until.Sub(since) / time.Hour)
	return storage.EventRangeStats{Since: since, Until: until, Messages: hours * 7, Handled: hours * 5, Errors: hours}, nil
}

func (s *stubRangeReader) LLMUsageSince(_ context.Context, since, until time.Time) (applog.UsageSummary, error) {
	if s.err != nil {
		return applog.UsageSummary{}, s.err
	}
	window := until.Sub(since)
	s.calls = append(s.calls, window)
	hours := int64(window / time.Hour)
	return applog.UsageSummary{
		Since:             since,
		Until:             until,
		Calls:             hours,
		InputTokens:       hours * 100,
		OutputTokens:      hours * 10,
		TotalTokens:       hours * 110,
		CachedInputTokens: hours * 40,
	}, nil
}

func newStatsRangeRouter(t *testing.T, reader *stubRangeReader, now time.Time) *gin.Engine {
	t.Helper()
	handler := NewStatsHandler(NewStatsCollector(), &fakeStatusProvider{})
	if reader != nil {
		handler.WithRangeReaders(reader, reader)
	}
	handler.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

func TestStatsRangesReturnsEveryWindow(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	reader := &stubRangeReader{}
	router := newStatsRangeRouter(t, reader, now)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/ranges", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var response StatsRangesResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(response.Ranges) != 3 {
		t.Fatalf("ranges = %d, want 3", len(response.Ranges))
	}
	// 窗口按 1h / 12h / 24h 的顺序给，前端照着渲染分段控件，顺序错了标签就对不上数。
	wantIDs := []string{"1h", "12h", "24h"}
	wantHours := []time.Duration{time.Hour, 12 * time.Hour, 24 * time.Hour}
	for i, rng := range response.Ranges {
		if rng.ID != wantIDs[i] {
			t.Fatalf("window[%d].ID = %q, want %q", i, rng.ID, wantIDs[i])
		}
		if reader.calls[i] != wantHours[i] {
			t.Fatalf("window[%d] asked for %s, want %s", i, reader.calls[i], wantHours[i])
		}
	}
	// 数值原样透传，不该在这一层被改写或换算。
	if got := response.Ranges[2].Usage.TotalTokens; got != 24*110 {
		t.Fatalf("24h TotalTokens = %d, want %d", got, 24*110)
	}
	if got := response.Ranges[2].Messages; got != 24*7 {
		t.Fatalf("24h Messages = %d, want %d", got, 24*7)
	}
	if !response.Until.Equal(now) {
		t.Fatalf("Until = %s, want %s", response.Until, now)
	}
}

func TestStatsRangesWithoutReaders(t *testing.T) {
	// 没有日志存储时必须报错。回一份零值的话，前端会把它显示成「这一小时没调用过」。
	router := newStatsRangeRouter(t, nil, time.Now())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/ranges", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestStatsRangesReportsReadFailure(t *testing.T) {
	router := newStatsRangeRouter(t, &stubRangeReader{err: fmt.Errorf("db closed")}, time.Now())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/ranges", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
