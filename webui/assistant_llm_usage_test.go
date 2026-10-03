package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/gin-gonic/gin"
)

type usageReportLogStore struct {
	filter       applog.UsageFilter
	since, until time.Time
	calls        int
	err          error
}

func (*usageReportLogStore) AppendLog(context.Context, applog.Entry) error { return nil }
func (s *usageReportLogStore) LLMUsageReport(_ context.Context, filter applog.UsageFilter, since, until time.Time) (applog.UsageReport, error) {
	s.filter, s.since, s.until = filter, since, until
	s.calls++
	return applog.UsageReport{Usage: applog.UsageSummary{Calls: 1, TotalTokens: 123}, Groups: []applog.GroupTokenUsage{}}, s.err
}

func TestGroupTokenUsageEndpointFiltersAndReportsFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &usageReportLogStore{}
	handler := &BotHandler{logs: store}
	router := gin.New()
	router.GET("/usage", handler.llmUsage)
	request := func(query string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/usage"+query, nil))
		return recorder
	}
	if result := request("?profile=bot-a&group_id=g&platform=telegram&hours=168"); result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if store.filter != (applog.UsageFilter{ProfileID: "bot-a", GroupID: "g", Platform: "telegram"}) || store.until.Sub(store.since) != 168*time.Hour {
		t.Fatalf("filter/window=%+v", store)
	}
	for _, hours := range []string{"0", "2161", "1.5", "invalid"} {
		before := store.calls
		if result := request("?hours=" + hours); result.Code != http.StatusBadRequest || store.calls != before {
			t.Fatalf("invalid hours reached storage: %s status=%d", hours, result.Code)
		}
	}
	store.err = errors.New("storage unavailable")
	if result := request(""); result.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure became success: %d", result.Code)
	}
	handler.logs = nil
	if result := request(""); result.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing storage became success: %d", result.Code)
	}
}
