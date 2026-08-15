package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

type consoleGroupListChannel struct {
	result map[string]any
	err    error
}

func (consoleGroupListChannel) Connect(context.Context, assistant.EventHandler) error { return nil }
func (consoleGroupListChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (consoleGroupListChannel) Status() assistant.ChannelStatus                       { return assistant.ChannelStatus{} }
func (consoleGroupListChannel) Close() error                                          { return nil }

func (c consoleGroupListChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, errors.New("unexpected OneBot action")
	}
	return c.result, c.err
}

func TestConsoleGroupsListsJoinedAndSavedGroups(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{
		"items": []any{
			map[string]any{"group_id": float64(10001), "group_name": "Alpha 群", "member_count": float64(12), "max_member_count": float64(200)},
			map[string]any{"group_id": "20002", "group_name": "Beta 群", "member_count": 34, "max_member_count": 500},
		},
	}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryQQBotGroupConfigStore()
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "20002", Enabled: false, EnabledSet: true, SystemPrompt: "专属人设"}, base); err != nil {
		t.Fatalf("SaveGroupConfig(20002) error = %v", err)
	}
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "30003", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatalf("SaveGroupConfig(30003) error = %v", err)
	}
	handler := NewQQBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := qqBotTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("assistant groups status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !response.LiveAvailable || response.Warning != "" || len(response.Groups) != 3 {
		t.Fatalf("response = %#v", response)
	}
	groups := make(map[string]consoleGroupItem, len(response.Groups))
	for _, group := range response.Groups {
		groups[group.GroupID] = group
	}
	if group := groups["10001"]; !group.Joined || group.Configured || group.GroupName != "Alpha 群" || group.MemberCount != 12 || group.MaxMemberCount != 200 || group.AvatarURL == "" {
		t.Fatalf("live unconfigured group = %#v", group)
	}
	if group := groups["20002"]; !group.Joined || !group.Configured || group.Enabled || group.SystemPrompt != "专属人设" || group.GroupName != "Beta 群" {
		t.Fatalf("live configured group = %#v", group)
	}
	if group := groups["30003"]; group.Joined || !group.Configured {
		t.Fatalf("saved group = %#v", group)
	}

	aliasRec := httptest.NewRecorder()
	router.ServeHTTP(aliasRec, httptest.NewRequest(http.MethodGet, "/api/qqbot/groups", nil))
	if aliasRec.Code != http.StatusOK {
		t.Fatalf("qqbot groups alias status = %d, body = %s", aliasRec.Code, aliasRec.Body.String())
	}
}

func TestConsoleGroupsFallsBackToSavedConfigWhenLiveListUnavailable(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{err: errors.New("not connected")}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryQQBotGroupConfigStore()
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "40004", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatalf("SaveGroupConfig() error = %v", err)
	}
	handler := NewQQBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := qqBotTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if response.LiveAvailable || response.Warning == "" || len(response.Groups) != 1 || response.Groups[0].GroupID != "40004" || !response.Groups[0].Configured {
		t.Fatalf("response = %#v", response)
	}
}

func TestConsoleGroupsSavesRecallReplyAutoDeletePolicy(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryQQBotGroupConfigStore()
	handler := NewQQBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := qqBotTestRouter(handler)

	body := `{"config":{"group_id":"50005","enabled":true,"enabled_set":true,"recall_reply_auto_delete_enabled":true,"recall_reply_auto_delete_delay_seconds":90}}`
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Config assistant.GroupConfig `json:"config"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Config.RecallReplyAutoDeleteEnabled == nil || !*response.Config.RecallReplyAutoDeleteEnabled || response.Config.RecallReplyTTLSeconds != 90 {
		t.Fatalf("response config = %#v", response.Config)
	}
	saved, ok := store.ConfigForGroup("50005")
	if !ok || saved.RecallReplyAutoDeleteEnabled == nil || !*saved.RecallReplyAutoDeleteEnabled || saved.RecallReplyTTLSeconds != 90 {
		t.Fatalf("saved config = %#v, ok = %v", saved, ok)
	}
}
