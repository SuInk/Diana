package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

type usageTestStore struct {
	calls        int
	since, until time.Time
	err          error
}

func (*usageTestStore) AppendLog(context.Context, applog.Entry) error { return nil }
func (s *usageTestStore) LLMUsageSince(_ context.Context, since, until time.Time) (applog.UsageSummary, error) {
	s.calls++
	s.since, s.until = since, until
	return applog.UsageSummary{Since: since, Until: until, Calls: 3, InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CachedInputTokens: 50}, s.err
}

func TestUsageToolOwnerOnly(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	s := &usageTestStore{}
	r.SetAppLogWriter(s)
	for _, role := range []string{"member", "admin", "owner"} {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "someone", SenderRole: role}
		tool := &dianaUsageTool{runtime: r, event: event}
		if _, err := tool.Run(context.Background(), nil); err == nil {
			t.Fatalf("group role %s bypassed owner check", role)
		}
		if allowed := r.allowedAgentToolNamesForEvent(event, r.relationshipPolicy(context.Background(), event)); allowed == nil || allowed[dianaUsageToolName] {
			t.Fatalf("tool exposed to %s", role)
		}
	}
	if s.calls != 0 {
		t.Fatal("unauthorized request read usage")
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	tool := &dianaUsageTool{runtime: r, event: MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, now: func() time.Time { return now }}
	body, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Usage applog.UsageSummary `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.Usage.TotalTokens != 120 || result.Usage.CachedInputTokens != 50 || !s.until.Equal(now) || !s.since.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("wrong usage: %s", body)
	}
	s.err = errors.New("storage failed")
	if body, err := tool.Run(context.Background(), nil); err == nil || body != "" {
		t.Fatal("failure reported as usage")
	}
	r.SetAppLogWriter(nil)
	if _, err := tool.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatal("missing storage reported as zero")
	}
}

type usageReportTestStore struct {
	usageTestStore
	filter applog.UsageFilter
}

func (s *usageReportTestStore) LLMUsageReport(_ context.Context, filter applog.UsageFilter, since, until time.Time) (applog.UsageReport, error) {
	s.calls++
	s.filter = filter
	s.since, s.until = since, until
	return applog.UsageReport{Usage: applog.UsageSummary{Calls: 1, TotalTokens: 123}}, s.err
}
func TestUsageToolCurrentGroupFilterAndInputValidation(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &usageReportTestStore{}
	runtime.SetAppLogWriter(store)
	tool := &dianaUsageTool{runtime: runtime, event: MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", Platform: "onebot-v11", GroupID: "g", UserID: "owner"}}
	if _, err := tool.Run(context.Background(), map[string]any{"scope": "current_group", "hours": float64(168)}); err != nil {
		t.Fatal(err)
	}
	if store.filter != (applog.UsageFilter{ProfileID: "bot-a", Platform: "onebot-v11", GroupID: "g"}) || store.until.Sub(store.since) != 168*time.Hour {
		t.Fatalf("wrong filter/window: %+v", store)
	}
	for _, input := range []map[string]any{{"scope": "current_group", "group_id": "other"}, {"scope": "all", "group_id": "g"}, {"scope": "group"}, {"hours": 1.5}, {"hours": 0}, {"hours": 2161}, {"bogus": true}} {
		before := store.calls
		if _, err := tool.Run(context.Background(), input); err == nil || store.calls != before {
			t.Fatalf("invalid input read storage: %v", input)
		}
	}
	tool.event.Kind = EventKindPrivate
	if _, err := tool.Run(context.Background(), map[string]any{"scope": "current_group"}); err == nil {
		t.Fatal("private query pretended to be a group")
	}
}
