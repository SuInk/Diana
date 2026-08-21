// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// TestBotHandlerConfigKeepsTokenHidden 验证对应功能场景。
func TestBotHandlerConfigKeepsTokenHidden(t *testing.T) {
	runtime := assistant.NewRuntime(
		assistant.BotConfig{
			Enabled:                 false,
			OneBotReverseWSEndpoint: "ws://127.0.0.1:18080/onebot/v11/ws",
			OneBotAccessToken:       "secret",
			NoneBotBridgeEnabled:    true,
			NoneBotBridgeEndpoint:   "ws://127.0.0.1:8080/onebot/v11/ws",
			NoneBotBridgeToken:      "nonebot-secret",
		},
		fakeChannel{},
		assistant.NewDefaultPluginManager(),
		nil,
		nil,
		nil,
		nil,
	)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	body := []byte(`{"enabled":false,"onebot_reverse_ws_endpoint":"ws://127.0.0.1:18080/onebot/v11/ws","nonebot_bridge_enabled":true,"nonebot_bridge_endpoint":"ws://127.0.0.1:8080/onebot/v11/ws","group_triggers":["Diana"],"disabled_groups":["123456"],"welcome_enabled":true,"welcome_message":"欢迎 {user_id}","request_timeout_ms":1000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload assistant.ConfigPayload
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.OneBotAccessToken != "" || !payload.OneBotAccessTokenConfigured {
		t.Fatalf("token leaked or flag wrong: %#v", payload)
	}
	if payload.NoneBotBridgeToken != "" || !payload.NoneBotBridgeTokenConfigured {
		t.Fatalf("nonebot token leaked or flag wrong: %#v", payload)
	}
	if len(payload.DisabledGroups) != 1 || payload.DisabledGroups[0] != "123456" {
		t.Fatalf("disabled groups wrong: %#v", payload.DisabledGroups)
	}
	if !payload.WelcomeEnabled || payload.WelcomeMessage != "欢迎 {user_id}" {
		t.Fatalf("welcome payload wrong: %#v", payload)
	}
	if runtime.Config().OneBotAccessToken != "secret" {
		t.Fatalf("stored token = %q", runtime.Config().OneBotAccessToken)
	}
	if runtime.Config().NoneBotBridgeToken != "nonebot-secret" {
		t.Fatalf("stored nonebot token = %q", runtime.Config().NoneBotBridgeToken)
	}
}

// TestBotHandlerPluginInstallAndEnable 验证对应功能场景。
func TestBotHandlerPluginInstallAndEnable(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/plugins/official.nonebot-plugin-resolver-go/enabled", bytes.NewReader([]byte(`{"enabled":false}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var state assistant.PluginState
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if state.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
}

func TestBotHandlerInstallsResolverDependency(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	calledWith := ""
	handler.installResolverDependency = func(_ context.Context, name string) (assistant.ResolverDependencyInstallResult, error) {
		calledWith = name
		dep := assistant.ResolverDependency{Name: name, Available: true, Version: "2026.08.09"}
		return assistant.ResolverDependencyInstallResult{
			Dependency: dep,
			Resolver:   []assistant.ResolverDependency{dep},
			Installer:  "test-installer",
		}, nil
	}
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/dependencies/yt-dlp/install", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if calledWith != "yt-dlp" {
		t.Fatalf("dependency = %q", calledWith)
	}
	var result assistant.ResolverDependencyInstallResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !result.Dependency.Available || result.Installer != "test-installer" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBotHandlerRejectsUnknownResolverDependency(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.installResolverDependency = func(context.Context, string) (assistant.ResolverDependencyInstallResult, error) {
		return assistant.ResolverDependencyInstallResult{}, assistant.ErrUnknownResolverDependency
	}
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/dependencies/not-allowed/install", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestBotHandlerPluginSettingsUpdateAndReject 验证对应功能场景。
func TestBotHandlerPluginSettingsUpdateAndReject(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/plugins/official.nonebot-plugin-resolver-go/settings", bytes.NewReader([]byte(`{"settings":{"fetch_title":false,"max_links":8}}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var state assistant.PluginState
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if state.Settings["fetch_title"] != false || state.Settings["max_links"] != float64(8) {
		t.Fatalf("Settings = %#v", state.Settings)
	}

	// 未知设置键返回 400。
	req = httptest.NewRequest(http.MethodPost, "/api/qqbot/plugins/official.nonebot-plugin-resolver-go/settings", bytes.NewReader([]byte(`{"settings":{"bogus":1}}`)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus key status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 不存在的插件返回 404。
	req = httptest.NewRequest(http.MethodPost, "/api/qqbot/plugins/missing/settings", bytes.NewReader([]byte(`{"settings":{}}`)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing plugin status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestBotHandlerRejectsShortTokens 验证对应功能场景。
func TestBotHandlerRejectsShortTokens(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	body := []byte(`{"enabled":false,"onebot_reverse_ws_endpoint":"ws://127.0.0.1:18080/onebot/v11/ws","onebot_access_token":"short","group_triggers":["Diana"],"request_timeout_ms":1000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestBotHandlerGroupTestSendsMessage 验证QQ群收发测试会调用当前 channel 发群消息。
func TestBotHandlerGroupTestSendsMessage(t *testing.T) {
	channel := &recordingFakeChannel{}
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return channel
	})
	handler.SetFeatureFlags(BotFeatureFlags{GroupTest: true})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/group-test", bytes.NewReader([]byte(`{"group_id":"123456","message":"测试消息"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(channel.calls) != 1 {
		t.Fatalf("calls = %#v", channel.calls)
	}
	call := channel.calls[0]
	if call.action != "send_group_msg" {
		t.Fatalf("action = %q", call.action)
	}
	if call.params["group_id"] != int64(123456) {
		t.Fatalf("group_id = %#v", call.params["group_id"])
	}
	segments, ok := call.params["message"].([]map[string]any)
	if !ok || len(segments) != 1 {
		t.Fatalf("message = %#v", call.params["message"])
	}
	if segments[0]["type"] != "text" {
		t.Fatalf("segment type = %#v", segments[0])
	}
	var payload groupTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !payload.Sent || payload.GroupID != "123456" || payload.Message != "测试消息" || payload.MessageID != "42" {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestBotHandlerGroupTestRequiresGroupID 验证QQ群收发测试必须填写群号。
func TestBotHandlerGroupTestRequiresGroupID(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.SetFeatureFlags(BotFeatureFlags{GroupTest: true})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/group-test", bytes.NewReader([]byte(`{"message":"测试消息"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestBotHandlerGroupTestRequiresNumericGroupID 验证群测试不会把明显非法群号透传给 OneBot。
func TestBotHandlerGroupTestRequiresNumericGroupID(t *testing.T) {
	channel := &recordingFakeChannel{}
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return channel
	})
	handler.SetFeatureFlags(BotFeatureFlags{GroupTest: true})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/qqbot/group-test", bytes.NewReader([]byte(`{"group_id":"abc","message":"测试消息"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(channel.calls) != 0 {
		t.Fatalf("calls = %#v", channel.calls)
	}
}

// TestBotHandlerGroupTestReturnsRecentGroupEvents 验证QQ群收发测试能读取指定群最近收到的消息。
func TestBotHandlerGroupTestReturnsRecentGroupEvents(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	if err := runtime.HandleEvent(context.Background(), assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		GroupID:    "123456",
		UserID:     "10001",
		RawMessage: "普通群消息",
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.SetFeatureFlags(BotFeatureFlags{GroupTest: true})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/qqbot/group-test?group_id=123456", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload groupTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(payload.RecentEvents) != 1 {
		t.Fatalf("RecentEvents = %#v", payload.RecentEvents)
	}
	if payload.RecentEvents[0].GroupID != "123456" || payload.RecentEvents[0].Text != "普通群消息" {
		t.Fatalf("RecentEvents[0] = %#v", payload.RecentEvents[0])
	}
}

// TestBotHandlerGroupTestDisabledByDefault 验证QQ群收发测试默认不暴露到正式环境。
func TestBotHandlerGroupTestDisabledByDefault(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/qqbot/group-test?group_id=123456", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/qqbot/features", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("features status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var flags BotFeatureFlags
	if err := json.NewDecoder(rec.Body).Decode(&flags); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if flags.GroupTest {
		t.Fatalf("GroupTest = true, want false")
	}
}

// botTestRouter 封装当前模块的 botTestRouter 逻辑。
func botTestRouter(handler *BotHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

func TestBotPlatforms(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/qqbot/platforms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("platforms = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Platforms []assistant.PlatformDefinition `json:"platforms"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode platforms: %v", err)
	}
	if len(body.Platforms) != 2 {
		t.Fatalf("platforms = %#v, want OneBot v11 and Telegram", body.Platforms)
	}
	if body.Platforms[0].ID != assistant.PlatformOneBotV11 || body.Platforms[0].Name != "OneBot v11" {
		t.Fatalf("OneBot platform = %#v", body.Platforms[0])
	}
	for _, platform := range body.Platforms {
		if platform.ID == "napcat" || platform.ID == "lagrange" || platform.ID == "go-cqhttp" {
			t.Fatalf("legacy implementation exposed as platform: %#v", platform)
		}
	}
}

func TestBotContextIsolationEndpointPersistsAndRebuildsChannels(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	profiles := NewMemoryBotProfileStore(assistant.DefaultBotConfig())
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.SetProfileStore(profiles)
	var rebuilt assistant.ProfileSet
	handler.SetChannelSetFactory(func(set assistant.ProfileSet) assistant.Channel {
		rebuilt = set
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/assistant/config/context-isolation", bytes.NewReader([]byte(`{"enabled":false}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if profiles.Profiles().PlatformContextsIsolated() || rebuilt.PlatformContextsIsolated() {
		t.Fatalf("stored=%v rebuilt=%v, want both false", profiles.Profiles().PlatformContextsIsolated(), rebuilt.PlatformContextsIsolated())
	}
	var payload assistant.ConfigPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.IsolatePlatformContexts == nil || *payload.IsolatePlatformContexts {
		t.Fatalf("response isolation=%#v, want false", payload.IsolatePlatformContexts)
	}
}

type fakeChannel struct{}

// Connect 封装当前模块的 Connect 逻辑。
func (fakeChannel) Connect(context.Context, assistant.EventHandler) error { return nil }

// Send 封装当前模块的 Send 逻辑。
func (c fakeChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }

type recordingFakeChannel struct {
	fakeChannel
	calls []apiCall
}

type apiCall struct {
	action string
	params map[string]any
}

// CallAPI 封装当前模块的 CallAPI 逻辑。
func (fakeChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

// CallAPI 记录 API 调用，并模拟 OneBot 标准的 message_id 返回。
func (c *recordingFakeChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, apiCall{action: action, params: params})
	return map[string]any{"message_id": int64(42)}, nil
}

// Status 返回当前状态快照。
func (fakeChannel) Status() assistant.ChannelStatus { return assistant.ChannelStatus{} }

// Close 释放当前对象持有的资源。
func (fakeChannel) Close() error { return nil }

// 依赖按插件分组返回，界面才能把浏览器那一格挂到网页渲染插件上，而不是继续
// 只认链接解析一个插件。
func TestBotHandlerGroupsPluginDependenciesByPlugin(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/assistant/plugins/dependencies", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Resolver []assistant.ResolverDependency            `json:"resolver"`
		Plugins  map[string][]assistant.ResolverDependency `json:"plugins"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(payload.Plugins[assistant.ResolverPluginID]) != len(payload.Resolver) {
		t.Fatalf("resolver group = %#v, compat field = %#v", payload.Plugins[assistant.ResolverPluginID], payload.Resolver)
	}
	browser := payload.Plugins[assistant.SandboxedBrowserPluginID]
	if len(browser) != 1 {
		t.Fatalf("browser group = %#v", browser)
	}
	// 装没装浏览器取决于跑测试的机器，但两种情况都必须能说清楚：装了要给出路径，
	// 没装要给出原因——只留一句「需手动安装」等于什么都没说。
	if browser[0].Available {
		if strings.TrimSpace(browser[0].Path) == "" {
			t.Fatalf("available browser without a path: %#v", browser[0])
		}
	} else if strings.TrimSpace(browser[0].Detail) == "" {
		t.Fatalf("unavailable browser without a reason: %#v", browser[0])
	}
	// 能不能一键装取决于跑测试的机器上有没有包管理器，但只要说能装，就必须说清用哪个装。
	if browser[0].Installable && strings.TrimSpace(browser[0].Installer) == "" {
		t.Fatalf("installable browser without an installer: %#v", browser[0])
	}
}
