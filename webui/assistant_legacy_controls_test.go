// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestRestoredGroupControlRoutes(t *testing.T) {
	channel := &restoredControlChannel{responses: map[string]map[string]any{
		"delete_msg":           {"status": "ok"},
		"get_group_root_files": {"files": []any{}},
		"get_login_info":       {"user_id": int64(10001)},
	}}
	handler := restoredControlHandler(channel)
	router := qqBotTestRouter(handler)

	recorder := performJSONRequest(router, http.MethodPost, "/api/assistant/group-test/recall", `{"message_id":"42"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("recall status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(channel.calls) != 1 || channel.calls[0].action != "delete_msg" || channel.calls[0].params["message_id"] != int64(42) {
		t.Fatalf("recall calls = %#v", channel.calls)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/qqbot/group-test/files?group_id=123456", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("files status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(channel.calls) != 2 || channel.calls[1].action != "get_group_root_files" || channel.calls[1].params["group_id"] != int64(123456) {
		t.Fatalf("files calls = %#v", channel.calls)
	}

	recorder = performJSONRequest(router, http.MethodPost, "/api/assistant/group-test/onebot", `{"action":"get_login_info"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("onebot status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	callCount := len(channel.calls)
	recorder = performJSONRequest(router, http.MethodPost, "/api/assistant/group-test/onebot", `{"action":"send_group_msg","params":{"group_id":123456}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe onebot status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(channel.calls) != callCount {
		t.Fatalf("unsafe OneBot action reached channel: %#v", channel.calls)
	}
}

func TestRestoredAutoInfoParsesOneBotData(t *testing.T) {
	channel := &restoredControlChannel{
		status: assistant.ChannelStatus{Connected: true, SelfID: "10001"},
		responses: map[string]map[string]any{
			"get_login_info": {
				"user_id":  int64(10001),
				"nickname": "Diana",
			},
			"get_group_list": {
				"data": []any{map[string]any{
					"group_id":         int64(123456),
					"group_name":       "测试群",
					"member_count":     float64(12),
					"max_member_count": float64(200),
				}},
			},
		},
	}
	router := qqBotTestRouter(restoredControlHandler(channel))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/auto-info", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var info qqbotAutoInfoResponse
	if err := json.NewDecoder(recorder.Body).Decode(&info); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if info.BotQQ != "10001" || info.Nickname != "Diana" || len(info.Groups) != 1 {
		t.Fatalf("auto info = %#v", info)
	}
	if info.Groups[0].GroupID != "123456" || info.Groups[0].MemberCount != 12 {
		t.Fatalf("group info = %#v", info.Groups[0])
	}
}

func TestRestoredFileParseAndUploadRoutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(path, []byte("Diana restored file parser"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	channel := &restoredControlChannel{responses: map[string]map[string]any{
		"upload_group_file": {"status": "ok"},
	}}
	handler := restoredControlHandler(channel)
	handler.SetLocalMediaSharer(fixedMediaSharer{url: "http://127.0.0.1/media/test"})
	router := qqBotTestRouter(handler)

	body, err := json.Marshal(groupTestFilePayload{Name: "fixture.txt", LocalPath: path})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	recorder := performJSONRequest(router, http.MethodPost, "/api/qqbot/group-test/file", string(body))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Diana restored file parser") {
		t.Fatalf("file parse status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body, err = json.Marshal(groupTestUploadFilePayload{GroupID: "123456", File: path, Name: "fixture.txt"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	recorder = performJSONRequest(router, http.MethodPost, "/api/assistant/group-test/upload-file", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	last := channel.calls[len(channel.calls)-1]
	if last.action != "upload_group_file" || last.params["file"] != "http://127.0.0.1/media/test" {
		t.Fatalf("upload call = %#v", last)
	}
}

func TestRestoredTaskListKeepsStatusAndNewestFirst(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().Round(time.Second)
	items := []assistant.Reminder{
		{ID: "old", Kind: assistant.ReminderKindMessage, OwnerID: "1", Message: "旧提醒", TriggerAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour)},
		{ID: "new", Kind: assistant.ReminderKindQuery, Platform: assistant.PlatformTelegram, ProfileID: "telegram-main", OwnerID: "1", Message: "新任务", TriggerAt: now.Add(time.Hour), IntervalSeconds: 3600, ConsecutiveFailures: 1, CreatedAt: now},
	}
	if err := store.SaveReminders(context.Background(), items); err != nil {
		t.Fatalf("SaveReminders() error = %v", err)
	}

	handler := restoredControlHandler(&restoredControlChannel{})
	handler.SetSQLiteStore(store)
	router := qqBotTestRouter(handler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/qqbot/tasks", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload qqbotTasksResponse
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(payload.Items) != 2 || payload.Items[0].ID != "new" || payload.Items[0].Kind != "schedule" || payload.Items[0].Status != "retrying" {
		t.Fatalf("tasks = %#v", payload.Items)
	}
	if payload.Items[0].Platform != assistant.PlatformTelegram || payload.Items[0].ProfileID != "telegram-main" || !payload.Items[0].ConsumesQuota {
		t.Fatalf("task source/quota = %#v", payload.Items[0])
	}
}

func TestRestoredOneTimeTaskReportsRetryingAndQuota(t *testing.T) {
	item := assistant.Reminder{Kind: assistant.ReminderKindMessage, ConsecutiveFailures: 2}
	if status := qqbotTaskStatus(item); status != "retrying" {
		t.Fatalf("status = %q, want retrying", status)
	}
	if !taskConsumesQuota(item) {
		t.Fatal("retrying one-time reminder should consume quota")
	}
	item.LastRunAt = time.Now()
	if taskConsumesQuota(item) {
		t.Fatal("used one-time reminder should release quota")
	}
}

func TestRepositoryWatchDoesNotConsumePersonalTaskQuota(t *testing.T) {
	item := assistant.Reminder{Kind: assistant.ReminderKindRepositoryWatch, IntervalSeconds: 30}
	if taskConsumesQuota(item) {
		t.Fatal("WebUI repository watch should not consume personal task quota")
	}
}

func TestRepositoryWatchCreateUsesSelectedProfileAndGroupTarget(t *testing.T) {
	base := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime := &capturingRepositoryWatchRuntime{Runtime: base}
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	profiles := NewMemoryQQBotProfileStore(assistant.BotConfig{
		Name: "通知机器人", Platform: assistant.PlatformOneBotV11, Enabled: true,
	})
	handler.SetProfileStore(profiles)
	profileID := profiles.Profiles().ActiveID
	router := qqBotTestRouter(handler)
	recorder := performJSONRequest(router, http.MethodPost, "/api/assistant/tasks/repository-watches", fmt.Sprintf(`{
		"repository":"acme/private","profile_id":%q,"destination":"group","group_id":"123456",
		"watch_commits":true,"watch_releases":true
	}`, profileID))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	input := runtime.createInput
	if input.OwnerID != "webui:"+profileID || input.UserID != "" || input.GroupID != "123456" || input.ProfileID != profileID {
		t.Fatalf("create input=%#v", input)
	}
	if input.Interval != 0 {
		t.Fatalf("handler should leave omitted interval for runtime default, got %s", input.Interval)
	}
}

func TestRepositoryWatchCreateUsesArbitraryPrivateTargetWithoutProfileOwner(t *testing.T) {
	base := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime := &capturingRepositoryWatchRuntime{Runtime: base}
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	profiles := NewMemoryQQBotProfileStore(assistant.BotConfig{Platform: assistant.PlatformOneBotV11, Enabled: true})
	handler.SetProfileStore(profiles)
	router := qqBotTestRouter(handler)
	recorder := performJSONRequest(router, http.MethodPost, "/api/assistant/tasks/repository-watches", fmt.Sprintf(`{
		"repository":"acme/private","profile_id":%q,"destination":"private","user_id":"998877","watch_commits":true
	}`, profiles.Profiles().ActiveID))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if runtime.createCalls != 1 || runtime.createInput.UserID != "998877" || runtime.createInput.GroupID != "" {
		t.Fatalf("runtime create calls=%d", runtime.createCalls)
	}
}

func TestRepositoryWatchCreateRequiresPrivateTarget(t *testing.T) {
	base := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime := &capturingRepositoryWatchRuntime{Runtime: base}
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	profiles := NewMemoryQQBotProfileStore(assistant.BotConfig{Platform: assistant.PlatformOneBotV11, Enabled: true})
	handler.SetProfileStore(profiles)
	router := qqBotTestRouter(handler)
	recorder := performJSONRequest(router, http.MethodPost, "/api/assistant/tasks/repository-watches", fmt.Sprintf(`{
		"repository":"acme/private","profile_id":%q,"destination":"private","watch_commits":true
	}`, profiles.Profiles().ActiveID))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "发送对象") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if runtime.createCalls != 0 {
		t.Fatalf("runtime create calls=%d", runtime.createCalls)
	}
}

func TestRestoredDashboardStatsAvailableWithoutStore(t *testing.T) {
	router := qqBotTestRouter(restoredControlHandler(&restoredControlChannel{}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/dashboard-stats", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var stats storage.DashboardStats
	if err := json.NewDecoder(recorder.Body).Decode(&stats); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if stats.Server.OS == "" || stats.Server.ProcessID == 0 || stats.Server.CPUCores == 0 {
		t.Fatalf("server stats = %#v", stats.Server)
	}
}

func restoredControlHandler(channel assistant.Channel) *QQBotHandler {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return channel })
	handler.SetFeatureFlags(QQBotFeatureFlags{GroupTest: true})
	return handler
}

func performJSONRequest(handler http.Handler, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

type restoredControlChannel struct {
	fakeChannel
	status    assistant.ChannelStatus
	responses map[string]map[string]any
	calls     []apiCall
}

type capturingRepositoryWatchRuntime struct {
	*assistant.Runtime
	createInput assistant.RepositoryWatchCreateInput
	createCalls int
}

func (r *capturingRepositoryWatchRuntime) CreateRepositoryWatch(_ context.Context, input assistant.RepositoryWatchCreateInput) (assistant.Reminder, error) {
	r.createCalls++
	r.createInput = input
	now := time.Now()
	return assistant.Reminder{
		ID: "watch-web", Kind: assistant.ReminderKindRepositoryWatch, Platform: input.Platform,
		ProfileID: input.ProfileID, OwnerID: input.OwnerID, UserID: input.UserID, GroupID: input.GroupID,
		Repository: input.Repository, WatchCommits: input.WatchCommits, WatchReleases: input.WatchReleases,
		IntervalSeconds: 30, TriggerAt: now.Add(30 * time.Second), CreatedAt: now,
	}, nil
}

func (r *capturingRepositoryWatchRuntime) UpdateRepositoryWatch(context.Context, string, string, assistant.RepositoryWatchUpdateInput) (assistant.Reminder, error) {
	return assistant.Reminder{}, nil
}

func (r *capturingRepositoryWatchRuntime) CancelRepositoryWatch(string, string) (assistant.Reminder, error) {
	return assistant.Reminder{}, nil
}

func (r *capturingRepositoryWatchRuntime) DeleteRepositoryWatch(string, string) (bool, error) {
	return true, nil
}

func (c *restoredControlChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, apiCall{action: action, params: params})
	if response, ok := c.responses[action]; ok {
		return response, nil
	}
	return map[string]any{}, nil
}

func (c *restoredControlChannel) Status() assistant.ChannelStatus { return c.status }

type fixedMediaSharer struct {
	url string
}

func (s fixedMediaSharer) Share(string, time.Duration) (string, bool) {
	return s.url, s.url != ""
}
