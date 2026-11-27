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

type stubOverviewReader struct {
	lastQuery storage.DashboardOverviewQuery
	data      storage.DashboardOverviewData
	err       error
}

func (s *stubOverviewReader) DashboardOverview(_ context.Context, query storage.DashboardOverviewQuery) (storage.DashboardOverviewData, error) {
	s.lastQuery = query
	if s.err != nil {
		return storage.DashboardOverviewData{}, s.err
	}
	return s.data, nil
}

func TestStatsOverviewSupportsLongRangesCustomBoundsAndGroupNames(t *testing.T) {
	shanghai := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 10, 12, 15, 30, 0, 0, shanghai)
	reader := &stubOverviewReader{
		data: storage.DashboardOverviewData{
			Current: storage.DashboardWindowSummary{Messages: 120, Handled: 90, Errors: 3},
			Groups:  []storage.DashboardGroupStat{{GroupID: "10001", TotalTokens: 4200}},
			Errors: storage.DashboardErrorOverview{
				EventErrors: 3,
				Recent:      []storage.DashboardErrorItem{{ID: "e1", Source: "event", GroupID: "10001", Message: "failed"}},
			},
		},
	}
	handler := NewStatsHandler(NewStatsCollector(), &fakeStatusProvider{}).
		WithOverviewReader(reader).
		WithGroupNames(func(context.Context) map[string]string {
			return map[string]string{"10001": "开发群"}
		})
	handler.now = func() time.Time { return now }
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	for _, rng := range []string{"today", "1h", "12h", "24h", "7d", "30d", "90d", "365d"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/overview?range="+rng+"&profile_id=bot-a", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("range %s status = %d, body = %s", rng, rec.Code, rec.Body.String())
		}
		if reader.lastQuery.ProfileID != "bot-a" {
			t.Fatalf("range %s query = %+v", rng, reader.lastQuery)
		}
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/overview?range=custom&since=2026-08-01T00:00:00Z&until=2026-09-01T00:00:00Z", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("custom status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp StatsOverviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Range != "custom" || resp.UTCOffset != "+08:00" || len(resp.Groups) != 1 || resp.Groups[0].GroupName != "开发群" || resp.Errors.Recent[0].GroupName != "开发群" {
		t.Fatalf("unexpected overview response: %+v", resp)
	}

	for _, bad := range []string{
		"/api/stats/overview?range=invalid",
		"/api/stats/overview?range=custom&since=2026-09-10T00:00:00Z&until=2026-09-01T00:00:00Z",
	} {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad query %s status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestStatsOverviewWithoutReaderReturns503(t *testing.T) {
	handler := NewStatsHandler(NewStatsCollector(), &fakeStatusProvider{})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/overview", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
