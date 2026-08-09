package assistant

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// TestPluginManagerInstallEnableRun 验证对应功能场景。
func TestPluginManagerInstallEnableRun(t *testing.T) {
	manager := NewPluginManager(testPlugin{})
	state, ok := manager.Get("test")
	if !ok {
		t.Fatal("plugin missing")
	}
	if state.Installed {
		t.Fatalf("Installed = true, want false for non built-in plugin")
	}

	if _, err := manager.Install("test"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	responses := manager.Run(context.Background(), PluginRequest{Text: "hello"})
	if len(responses) != 1 || responses[0].Context != "ctx: hello" {
		t.Fatalf("responses = %#v", responses)
	}

	if _, err := manager.SetEnabled("test", false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if responses := manager.Run(context.Background(), PluginRequest{Text: "hello"}); len(responses) != 0 {
		t.Fatalf("disabled responses = %#v", responses)
	}
}

// TestResolverPluginExtractsKnownPlatformContext 验证对应功能场景。
func TestResolverPluginExtractsKnownPlatformContext(t *testing.T) {
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})})

	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "看这个 https://www.bilibili.com/video/BV1xx411c7mD"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if !strings.Contains(resp.Context, "Bilibili") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

// TestDefaultPluginManagerIncludesFileParser 验证对应功能场景。
func TestDefaultPluginManagerIncludesFileParser(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get("official.file-parser-go")
	if !ok {
		t.Fatal("file parser plugin missing")
	}
	if !state.Installed || !state.Enabled {
		t.Fatalf("file parser state = %#v", state)
	}
}

// TestDefaultPluginManagerDoesNotExposeLLMConfigCommand 验证内置命令不再作为插件展示。
func TestDefaultPluginManagerDoesNotExposeLLMConfigCommand(t *testing.T) {
	manager := NewDefaultPluginManager()
	if state, ok := manager.Get("official.llm-config-skill"); ok {
		t.Fatalf("legacy llm config plugin still exposed: %#v", state)
	}
}

// TestPluginManagerRestoreKeepsBuiltInDisabledChoice 验证对应功能场景。
func TestPluginManagerRestoreKeepsBuiltInDisabledChoice(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		"official.file-parser-go": {
			Enabled: false,
		},
	})
	state, ok := manager.Get("official.file-parser-go")
	if !ok {
		t.Fatal("file parser plugin missing")
	}
	if !state.Installed {
		t.Fatalf("Installed = false, want true")
	}
	if state.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
}

// TestFileParserPluginParsesTextFileURL 验证对应功能场景。
func TestFileParserPluginParsesTextFileURL(t *testing.T) {
	plugin := NewFileParserPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("hello\nworld")),
		}, nil
	})})

	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "看文件 https://example.com/report.txt"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if !strings.Contains(resp.Context, "report.txt") || !strings.Contains(resp.Context, "hello") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

// TestFileParserPluginIgnoresUnsupportedURL 验证对应功能场景。
func TestFileParserPluginIgnoresUnsupportedURL(t *testing.T) {
	plugin := NewFileParserPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "图片 https://example.com/a.png"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil", resp)
	}
}

// TestLLMConfigPluginUpdatesProviderAndModel 验证对应功能场景。
func TestLLMConfigPluginUpdatesProviderAndModel(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "main",
			Profiles: []llm.Profile{
				{
					ID:   "main",
					Name: "主配置",
					Config: llm.ProviderConfig{
						Provider: llm.ProviderOpenAICompatible,
						APIKey:   "valid-key",
						Model:    "gp5.5",
					},
				},
			},
		},
	}
	logs := &captureAppLogs{}

	resp, err := handleLLMConfigRequest(context.Background(), PluginRequest{
		Event:    MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "20002"},
		Text:     "把提供商切到 Gemini，模型换成 gemini-2.5-pro",
		OwnerID:  "10001",
		LLMStore: store,
		AppLogs:  logs,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled || !strings.Contains(resp.Reply, "已更新当前 LLM") {
		t.Fatalf("resp = %#v", resp)
	}
	got := store.Current()
	if got.Provider != llm.ProviderGemini || got.Model != "gemini-2.5-pro" {
		t.Fatalf("current = %#v", got)
	}
	if len(logs.entries) != 1 {
		t.Fatalf("logs = %#v", logs.entries)
	}
	if logs.entries[0].Kind != applog.KindOperation || logs.entries[0].Actor != "qq:10001" {
		t.Fatalf("log entry = %#v", logs.entries[0])
	}
	if logs.entries[0].Metadata["group_id"] != "20002" || logs.entries[0].Metadata["new_model"] != "gemini-2.5-pro" {
		t.Fatalf("log metadata = %#v", logs.entries[0].Metadata)
	}
}

// TestLLMConfigPluginUpdatesModelOnly 验证对应功能场景。
func TestLLMConfigPluginUpdatesModelOnly(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "main",
			Profiles: []llm.Profile{
				{
					ID:   "main",
					Name: "主配置",
					Config: llm.ProviderConfig{
						Provider: llm.ProviderOpenAICompatible,
						APIKey:   "valid-key",
						Model:    "gp5.5",
					},
				},
			},
		},
	}
	resp, err := handleLLMConfigRequest(context.Background(), PluginRequest{
		Event:    MessageEvent{UserID: "10001"},
		Text:     "把模型换成 gpt-4.1-mini",
		OwnerID:  "10001",
		LLMStore: store,
		LLMModelLister: func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
			return []llm.ModelInfo{{ID: "gp5.5"}, {ID: "gpt-4.1-mini"}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	got := store.Current()
	if got.Provider != llm.ProviderOpenAICompatible || got.Model != "gpt-4.1-mini" {
		t.Fatalf("current = %#v", got)
	}
}

// TestLLMConfigPluginRejectsModelOutsideList 验证对应功能场景。
func TestLLMConfigPluginRejectsModelOutsideList(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "main",
			Profiles: []llm.Profile{
				{
					ID:   "main",
					Name: "主配置",
					Config: llm.ProviderConfig{
						Provider: llm.ProviderOpenAICompatible,
						APIKey:   "valid-key",
						Model:    "gp5.5",
					},
				},
			},
		},
	}
	resp, err := handleLLMConfigRequest(context.Background(), PluginRequest{
		Event:    MessageEvent{UserID: "10001"},
		Text:     "把模型换成 gemini-9-ultra",
		OwnerID:  "10001",
		LLMStore: store,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled || !strings.Contains(resp.Reply, "不在") {
		t.Fatalf("resp = %#v", resp)
	}
	if got := store.Current(); got.Provider != llm.ProviderOpenAICompatible || got.Model != "gp5.5" {
		t.Fatalf("current = %#v", got)
	}
}

// TestLLMConfigPluginRejectsNonOwner 验证对应功能场景。
func TestLLMConfigPluginRejectsNonOwner(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "main",
			Profiles: []llm.Profile{
				{ID: "main", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "valid-key", Model: "gp5.5"}},
			},
		},
	}
	logs := &captureAppLogs{}

	resp, err := handleLLMConfigRequest(context.Background(), PluginRequest{
		Event:    MessageEvent{UserID: "20002"},
		Text:     "把模型换成 gpt-4.1-mini",
		OwnerID:  "10001",
		LLMStore: store,
		AppLogs:  logs,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Reply, "只有主人") {
		t.Fatalf("resp = %#v", resp)
	}
	if got := store.Current(); got.Model != "gp5.5" {
		t.Fatalf("current = %#v", got)
	}
	if len(logs.entries) != 1 || logs.entries[0].Kind != applog.KindError || logs.entries[0].Actor != "qq:20002" {
		t.Fatalf("logs = %#v", logs.entries)
	}
}

// TestLLMConfigPluginIgnoresModelQuestion 验证对应功能场景。
func TestLLMConfigPluginIgnoresModelQuestion(t *testing.T) {
	resp, err := handleLLMConfigRequest(context.Background(), PluginRequest{
		Event:   MessageEvent{UserID: "10001"},
		Text:    "怎么用 gpt-4.1-mini 写代码？",
		OwnerID: "10001",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil", resp)
	}
}

// TestPluginManagerUpdateSettingsValidatesAndClamps 验证对应功能场景。
func TestPluginManagerUpdateSettingsValidatesAndClamps(t *testing.T) {
	manager := NewDefaultPluginManager()
	resolverID := "official.nonebot-plugin-resolver-go"

	state, err := manager.UpdateSettings(resolverID, map[string]any{
		"fetch_title": false,
		"max_links":   float64(50),
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if got := state.Settings["fetch_title"]; got != false {
		t.Fatalf("fetch_title = %#v, want false", got)
	}
	// 超出 Max 的数字应该被夹回上限而不是报错。
	if got := state.Settings["max_links"]; got != float64(20) {
		t.Fatalf("max_links = %#v, want 20", got)
	}

	if _, err := manager.UpdateSettings(resolverID, map[string]any{"no_such_key": true}); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := manager.UpdateSettings(resolverID, map[string]any{"fetch_title": "yes"}); err == nil {
		t.Fatal("wrong type accepted")
	}
	// multi_select：合法勾选去重保存，未知选项拒绝。
	if state, err := manager.UpdateSettings(resolverID, map[string]any{"exclude_platforms": []any{"weibo", "douyin", "weibo"}}); err != nil {
		t.Fatalf("multi_select UpdateSettings() error = %v", err)
	} else if got, ok := state.Settings["exclude_platforms"].([]string); !ok || len(got) != 2 {
		t.Fatalf("exclude_platforms = %#v", state.Settings["exclude_platforms"])
	}
	if _, err := manager.UpdateSettings(resolverID, map[string]any{"exclude_platforms": []any{"douyin", "netflix"}}); err == nil {
		t.Fatal("unknown platform option accepted")
	}
	if _, err := manager.UpdateSettings("missing", map[string]any{"a": 1}); err == nil {
		t.Fatal("missing plugin accepted")
	}
	state, err = manager.UpdateSettings(resolverID, map[string]any{})
	if err != nil {
		t.Fatalf("UpdateSettings(reset) error = %v", err)
	}
	if len(state.Settings) != 0 {
		t.Fatalf("Settings after reset = %#v, want empty", state.Settings)
	}
}

// TestPluginManagerRestoreSanitizesSettings 验证对应功能场景。
func TestPluginManagerRestoreSanitizesSettings(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		"official.nonebot-plugin-resolver-go": {
			Installed: true,
			Enabled:   true,
			Settings: map[string]any{
				"fetch_title":     false,       // 合法，保留
				"max_links":       float64(99), // 超上限，夹回 20
				"removed_key":     "stale",     // 已下线键，丢弃
				"timeout_seconds": "slow",      // 类型错误，丢弃
			},
		},
	})
	state, ok := manager.Get("official.nonebot-plugin-resolver-go")
	if !ok {
		t.Fatal("resolver plugin missing")
	}
	if got := state.Settings["fetch_title"]; got != false {
		t.Fatalf("fetch_title = %#v, want false", got)
	}
	if got := state.Settings["max_links"]; got != float64(20) {
		t.Fatalf("max_links = %#v, want 20", got)
	}
	if _, ok := state.Settings["removed_key"]; ok {
		t.Fatalf("removed_key survived: %#v", state.Settings)
	}
	if _, ok := state.Settings["timeout_seconds"]; ok {
		t.Fatalf("invalid timeout survived: %#v", state.Settings)
	}
}

// TestPluginManagerRunInjectsEffectiveSettings 验证对应功能场景。
func TestPluginManagerRunInjectsEffectiveSettings(t *testing.T) {
	probe := &settingsProbePlugin{}
	manager := NewPluginManager(probe)
	if _, err := manager.Install("probe"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := manager.UpdateSettings("probe", map[string]any{"limit": float64(7)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}

	manager.Run(context.Background(), PluginRequest{Text: "hi"})
	// 覆盖值生效，未覆盖的键取声明默认值。
	if got := probe.seen.Int("limit", -1); got != 7 {
		t.Fatalf("limit = %d, want 7", got)
	}
	if got := probe.seen.Bool("verbose", false); got != true {
		t.Fatalf("verbose = %v, want default true", got)
	}
}

// TestResolverPluginRespectsSettings 验证对应功能场景。
func TestResolverPluginRespectsSettings(t *testing.T) {
	requests := 0
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("<title>页面</title>")),
		}, nil
	})})

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: "两个链接 https://www.bilibili.com/video/BV1 和 https://github.com/foo/bar",
		Settings: SettingValues{
			"fetch_title": false,
			"max_links":   float64(1),
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	// 关闭抓取标题后不应发起任何 HTTP 请求。
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(resp.Context, "Bilibili") {
		t.Fatalf("Context = %q", resp.Context)
	}
	// max_links=1 时第二个链接被忽略。
	if strings.Contains(resp.Context, "GitHub") {
		t.Fatalf("Context should drop second link: %q", resp.Context)
	}
}

// TestFileParserPluginRespectsMaxFileKB 验证对应功能场景。
func TestFileParserPluginRespectsMaxFileKB(t *testing.T) {
	large := strings.Repeat("a", 65*1024)
	plugin := NewFileParserPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "text/plain")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(large)),
		}, nil
	})})

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     "看下 https://example.com/big.txt",
		Settings: SettingValues{"max_file_kb": float64(64)},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if !strings.Contains(resp.Context, "exceeds") {
		t.Fatalf("Context = %q, want size-limit rejection", resp.Context)
	}
}

type testPlugin struct{}

type settingsProbePlugin struct {
	seen SettingValues
}

// Manifest 返回设置探针插件清单。
func (*settingsProbePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:   "probe",
		Name: "Probe",
		Settings: []PluginSettingSpec{
			{Key: "limit", Label: "Limit", Type: PluginSettingTypeNumber, Default: 3, Min: settingRange(1), Max: settingRange(10)},
			{Key: "verbose", Label: "Verbose", Type: PluginSettingTypeBool, Default: true},
		},
	}
}

// Handle 记录运行时注入的生效设置。
func (p *settingsProbePlugin) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	p.seen = req.Settings
	return &PluginResponse{Handled: true}, nil
}

type captureAppLogs struct {
	entries []applog.Entry
}

// AppendLog 封装当前模块的 AppendLog 逻辑。
func (c *captureAppLogs) AppendLog(_ context.Context, entry applog.Entry) error {
	c.entries = append(c.entries, entry)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 封装当前模块的 RoundTrip 逻辑。
func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

// Manifest 返回插件清单信息。
func (testPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "test", Name: "Test"}
}

// Handle 处理当前插件请求。
func (testPlugin) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	return &PluginResponse{Handled: true, Context: "ctx: " + req.Text}, nil
}
