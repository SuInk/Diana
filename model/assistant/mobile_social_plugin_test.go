// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/mobilectl"
)

type fakeMobileDriver struct {
	mu           sync.Mutex
	platform     string
	currentApp   string
	screensByApp map[string][][]mobilectl.Element
	pageIndex    int
	pngBytes     []byte
	pressedKeys  []string
	tapped       [][2]int
}

func newFakeMobileDriver(t *testing.T, platform string) *fakeMobileDriver {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 390, 844))
	img.Set(1, 1, color.RGBA{R: 30, G: 120, B: 80, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return &fakeMobileDriver{
		platform:     platform,
		pngBytes:     buf.Bytes(),
		screensByApp: map[string][][]mobilectl.Element{},
	}
}

func (d *fakeMobileDriver) ListDevices(context.Context) ([]mobilectl.DeviceInfo, error) {
	return []mobilectl.DeviceInfo{{
		ID:       "iphone-test-1",
		Name:     "iPhone 16 Pro",
		Platform: d.platform,
		State:    "online",
		Backend:  mobilectl.BackendWDA,
	}}, nil
}

func (d *fakeMobileDriver) LaunchApp(_ context.Context, _, appID string) error {
	d.mu.Lock()
	d.currentApp = appID
	d.pageIndex = 0
	d.mu.Unlock()
	return nil
}

func (d *fakeMobileDriver) OpenURL(context.Context, string, string) error { return nil }

func (d *fakeMobileDriver) Screenshot(context.Context, string) ([]byte, error) {
	return d.pngBytes, nil
}

func (d *fakeMobileDriver) Elements(context.Context, string) ([]mobilectl.Element, int, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	pages := d.screensByApp[d.currentApp]
	if len(pages) == 0 {
		return nil, 390, 844, nil
	}
	idx := d.pageIndex
	if idx >= len(pages) {
		idx = len(pages) - 1
	}
	return pages[idx], 390, 844, nil
}

func (d *fakeMobileDriver) Tap(_ context.Context, _ string, x, y int) error {
	d.mu.Lock()
	d.tapped = append(d.tapped, [2]int{x, y})
	d.mu.Unlock()
	return nil
}

func (d *fakeMobileDriver) Swipe(context.Context, string, int, int, int, int, int) error {
	d.mu.Lock()
	d.pageIndex++
	d.mu.Unlock()
	return nil
}

func (d *fakeMobileDriver) TypeText(context.Context, string, string) error { return nil }

func (d *fakeMobileDriver) PressKey(_ context.Context, _, key string) error {
	d.mu.Lock()
	d.pressedKeys = append(d.pressedKeys, key)
	d.mu.Unlock()
	return nil
}

func (d *fakeMobileDriver) Close() error { return nil }

type scriptedMobileLLM struct {
	mu        sync.Mutex
	curateOut string
	shareOut  string
	visionOut string
}

func (m *scriptedMobileLLM) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var combined strings.Builder
	for _, msg := range req.Messages {
		combined.WriteString(msg.Content + "\n")
	}
	promptText := combined.String()
	switch {
	case strings.Contains(promptText, "读取手机社媒信息流截图"):
		return &llm.GenerateResponse{Text: m.visionOut}, nil
	case strings.Contains(promptText, `"interest_topics"`) || strings.Contains(promptText, "breaking|casual"):
		return &llm.GenerateResponse{Text: m.curateOut}, nil
	case strings.Contains(promptText, "刚刷到的重磅新鲜事"):
		return &llm.GenerateResponse{Text: m.shareOut}, nil
	default:
		// Account safety audit or other auxiliary prompts.
		return &llm.GenerateResponse{Text: `{"safe":true,"pass":true,"decision":"allow"}`}, nil
	}
}

func newMobileSocialTestRuntime(cfg BotConfig, channel *recordingChannel, mgr *PluginManager, fakeLLM *scriptedMobileLLM) *Runtime {
	var store LLMProfileStore
	if fakeLLM != nil {
		store = &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible,
			APIKey:   "test-key",
			BaseURL:  "http://localhost/v1",
			Model:    "gpt-test",
		})}
	}
	rt := NewRuntime(cfg, channel, mgr, store, nil, nil, nil)
	if fakeLLM != nil {
		rt.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
			return fakeLLM, nil
		})
	}
	return rt
}

func TestMobileSocialPatrolBreakingAndCasualPool(t *testing.T) {
	// Mock Hacker News + V2EX HTTP server.
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/topstories.json":
			_, _ = w.Write([]byte(`[101]`))
		case "/v0/item/101.json":
			_, _ = w.Write([]byte(`{"id":101,"title":"SQLite 4 experimental engine notes","url":"https://example.com/sqlite","score":412,"descendants":128}`))
		case "/api/topics/hot.json":
			_, _ = w.Write([]byte(`[{"id":202,"title":"Mac mini 跑本地量化大模型实测分享","content":"M4 芯片内存带宽表现不错","url":"https://www.v2ex.com/t/202","replies":86,"node":{"title":"程序员"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	fakeDrv := newFakeMobileDriver(t, mobilectl.PlatformIOS)
	fakeDrv.screensByApp["com.atebits.Tweetie2"] = [][]mobilectl.Element{
		{
			{Type: "Cell", Label: "NVIDIA 官方快讯", Text: "重磅官宣：英伟达核心创始高管团队今日正式回归坐镇 AI 架构", X: 16, Y: 140, Width: 358, Height: 160},
		},
	}
	fakeDrv.screensByApp["com.xingin.discover"] = [][]mobilectl.Element{
		{
			{Type: "Cell", Label: "数码测评", Text: "桌面理线与磁吸充电底座改造记录", X: 16, Y: 150, Width: 358, Height: 180},
		},
	}
	// Leave Heybox empty in accessibility tree to exercise vision screenshot fallback!
	fakeDrv.screensByApp["com.max.xiaoheihe"] = [][]mobilectl.Element{}

	fakeLLM := &scriptedMobileLLM{
		visionOut: `{"posts":["小黑盒热榜：Steam 周末特惠黑神话史低消息汇总"]}`,
	}

	channel := &recordingChannel{}
	cfg := DefaultBotConfig()
	cfg.ID = "bot-main"
	cfg.Name = "Diana"
	cfg.Enabled = true
	cfg.Platform = PlatformOneBotV11
	cfg.BotAccount = "10000"
	cfg.OwnerID = "10001"
	cfg.ReplySafetyMasterEnabled = boolPointer(false)

	mgr := NewDefaultPluginManager()
	rawPlugin, _, _ := mgr.PluginForConfiguration(mobileSocialPluginID)
	if rawPlugin == nil {
		t.Fatalf("expected %s registered in NewDefaultPluginManager", mobileSocialPluginID)
	}
	plugin := rawPlugin.(*MobileSocialPlugin)
	plugin.httpClient = apiSrv.Client()
	plugin.hnBaseURL = apiSrv.URL + "/v0"
	plugin.v2exBaseURL = apiSrv.URL
	plugin.settleDelay = 1 * time.Millisecond
	plugin.SetControllerForTest(mobilectl.NewControllerWithDriver(mobilectl.Config{Backend: mobilectl.BackendWDA}, fakeDrv))

	rt := newMobileSocialTestRuntime(cfg, channel, mgr, fakeLLM)
	// Fix clock to 14:30 afternoon (outside 23:00-08:00 quiet hours).
	fixedNow := time.Date(2026, 11, 24, 14, 30, 0, 0, time.UTC)
	rt.now = func() time.Time { return fixedNow }

	if _, err := mgr.SetEnabledForProfile(mobileSocialPluginID, cfg.ID, true); err != nil {
		t.Fatalf("SetEnabledForProfile: %v", err)
	}
	if _, err := mgr.UpdateSettingsForProfile(mobileSocialPluginID, cfg.ID, map[string]any{
		mobileSocialSettingTargetGroups: "20001",
		mobileSocialSettingSwipeScreens: 1,
	}, nil); err != nil {
		t.Fatalf("UpdateSettingsForProfile: %v", err)
	}

	// First collect once to inspect deterministic hashes or craft curateOut dynamically:
	rawItems, warnings := plugin.collectAllSources(context.Background(), SettingValues{
		mobileSocialSettingSwipeScreens:   1,
		mobileSocialSettingVisionFallback: true,
	})
	if len(warnings) > 0 {
		t.Fatalf("unexpected collect warnings: %v", warnings)
	}
	if len(rawItems) < 4 {
		t.Fatalf("expected items from X, XHS, Heybox (vision), HN, V2EX; got %d: %+v", len(rawItems), rawItems)
	}

	var xHash, v2exHash string
	for _, it := range rawItems {
		if it.Source == mobileSourceX {
			xHash = it.Hash
		}
		if it.Source == mobileSourceV2EX {
			v2exHash = it.Hash
		}
	}
	if xHash == "" || v2exHash == "" {
		t.Fatalf("missing expected X or V2EX hash in %+v", rawItems)
	}

	curatePayload, _ := json.Marshal(map[string]any{
		"items": []map[string]any{
			{
				"hash":    xHash,
				"tier":    "breaking",
				"score":   95,
				"title":   "英伟达核心创始高管团队今日正式回归",
				"summary": "X 热帖官宣英伟达核心架构团队回归坐镇下一代 AI 芯片。",
				"tags":    []string{"英伟达", "nvidia", "AI芯片"},
				"angle":   "刚刷推看到英伟达那边大动作",
			},
			{
				"hash":    v2exHash,
				"tier":    "casual",
				"score":   76,
				"title":   "Mac mini 跑本地量化大模型实测",
				"summary": "V2EX 网友实测 M4 Mac mini 跑本地量化模型速度稳定。",
				"tags":    []string{"mac mini", "本地大模型", "v2ex"},
				"angle":   "V2EX 上正好有人测了 Mac mini 跑模型",
			},
		},
	})
	fakeLLM.curateOut = string(curatePayload)
	fakeLLM.shareOut = "卧槽刚刷 X 看到，英伟达核心创始团队今天正式回归坐镇下一代 AI 架构了，这波老黄又要发力了？"

	patrolRes, err := plugin.PatrolOnce(context.Background())
	if err != nil {
		t.Fatalf("PatrolOnce failed: %v", err)
	}
	if patrolRes.BreakingAdded != 1 || patrolRes.CasualAdded != 1 || patrolRes.PushedGroups != 1 {
		t.Fatalf("unexpected patrol result: %+v", patrolRes)
	}

	channel.mu.Lock()
	sentCount := len(channel.sent)
	firstText := ""
	if sentCount > 0 {
		firstText = channel.sent[0].Text
	}
	channel.mu.Unlock()
	if sentCount != 1 || !strings.Contains(firstText, "英伟达") {
		t.Fatalf("expected breaking news pushed to group 20001, got count=%d first=%q", sentCount, firstText)
	}

	// Verify phone pressed Home after patrol so screen doesn't stay inside social app.
	fakeDrv.mu.Lock()
	keys := append([]string(nil), fakeDrv.pressedKeys...)
	fakeDrv.mu.Unlock()
	if len(keys) == 0 || keys[len(keys)-1] != "home" {
		t.Fatalf("expected home key after mobile patrol, got %+v", keys)
	}

	// Verify RuntimeStatus reflects connected iPhone & candidate pool.
	status, hasStatus := plugin.RuntimeStatus(context.Background())
	if !hasStatus || !strings.Contains(status.Summary, "iPhone 16 Pro") {
		t.Fatalf("unexpected RuntimeStatus: has=%v status=%+v", hasStatus, status)
	}

	// Verify Casual Pool injection:
	// 1) Unrelated group chat should NOT inject anything.
	groupEvent := MessageEvent{
		Kind:      EventKindGroup,
		Platform:  PlatformOneBotV11,
		ProfileID: cfg.ID,
		GroupID:   "20001",
		UserID:    "10002",
	}
	if unrelated := rt.mobileSocialPoolContext(context.Background(), groupEvent, "今晚大家吃什么宵夜"); unrelated != "" {
		t.Fatalf("expected empty context for unrelated chat, got: %s", unrelated)
	}

	// 2) Related group chat ("想买台 Mac mini 跑本地大模型") SHOULD inject the V2EX casual candidate!
	related := rt.mobileSocialPoolContext(context.Background(), groupEvent, "最近想买台 Mac mini 在家跑本地大模型靠谱吗")
	if !strings.Contains(related, "Mac mini 跑本地量化大模型实测") || !strings.HasPrefix(related, "【近期刷到的社媒与社区新鲜事（可选素材）】") {
		t.Fatalf("expected matching V2EX candidate in pool context, got: %q", related)
	}

	// 3) Cold-start icebreaker (`pickMobileSocialColdStartNews`) also surfaces unshared casual items.
	coldNews := rt.pickMobileSocialColdStartNews(groupEvent, []string{"群友A：最近想折腾一下本地部署"})
	if !strings.Contains(coldNews.Results, "Mac mini") {
		t.Fatalf("expected cold-start news to include Mac mini item, got: %+v", coldNews)
	}

	// 4) Once bot replies mentioning Mac mini + 本地大模型, it marks the item shared for group 20001!
	rt.markMobileSocialSharedFromReply(groupEvent, "我刚在 V2EX 看到有人实测 Mac mini 跑本地量化大模型，内存带宽挺稳的")
	if afterShare := rt.mobileSocialPoolContext(context.Background(), groupEvent, "Mac mini 本地大模型还有啥说法"); afterShare != "" {
		t.Fatalf("expected candidate marked as shared after bot reply, still got: %s", afterShare)
	}
}

func TestMobileDeviceToolOwnerAndSafeModeIsolation(t *testing.T) {
	fakeDrv := newFakeMobileDriver(t, mobilectl.PlatformIOS)
	fakeDrv.screensByApp["com.xingin.discover"] = [][]mobilectl.Element{
		{{Type: "Button", Label: "搜索框", Text: "热门数码", X: 40, Y: 100, Width: 200, Height: 60}},
	}

	channel := &recordingChannel{}
	cfg := DefaultBotConfig()
	cfg.ID = "bot-main"
	cfg.Enabled = true
	cfg.Platform = PlatformOneBotV11
	cfg.OwnerID = "10001"
	cfg.AgentEnabled = true

	mgr := NewDefaultPluginManager()
	rawPlugin, _, _ := mgr.PluginForConfiguration(mobileSocialPluginID)
	plugin := rawPlugin.(*MobileSocialPlugin)
	plugin.SetControllerForTest(mobilectl.NewControllerWithDriver(mobilectl.Config{Backend: mobilectl.BackendWDA}, fakeDrv))
	_, _ = mgr.SetEnabledForProfile(mobileSocialPluginID, cfg.ID, true)

	rt := newMobileSocialTestRuntime(cfg, channel, mgr, nil)

	ownerEvent := MessageEvent{
		Kind:      EventKindPrivate,
		Platform:  PlatformOneBotV11,
		ProfileID: cfg.ID,
		UserID:    "10001",
	}
	ownerRel := rt.relationshipPolicy(context.Background(), ownerEvent)
	ownerReg, err := rt.newReplyAgentRegistry(context.Background(), cfg, ownerEvent, ownerRel, nil)
	if err != nil {
		t.Fatalf("newReplyAgentRegistry owner: %v", err)
	}
	toolVal, ok := ownerReg.Get(mobileDeviceToolName)
	if !ok {
		t.Fatalf("expected owner in standard mode to have %s tool", mobileDeviceToolName)
	}

	// Exercise mobile_device actions: devices, open_app, elements, tap by element_id+observation, screenshot (with image part), swipe, type, key.
	ctx := context.Background()
	if out, err := toolVal.Run(ctx, map[string]any{"action": "devices"}); err != nil || !strings.Contains(out, "iPhone 16 Pro") {
		t.Fatalf("devices failed: out=%s err=%v", out, err)
	}
	if _, err := toolVal.Run(ctx, map[string]any{"action": "open_app", "app": "xiaohongshu"}); err != nil {
		t.Fatalf("open_app failed: %v", err)
	}
	elemOut, err := toolVal.Run(ctx, map[string]any{"action": "elements"})
	if err != nil {
		t.Fatalf("elements failed: %v", err)
	}
	var snap mobilectl.ElementsSnapshot
	if err := json.Unmarshal([]byte(elemOut), &snap); err != nil || len(snap.Elements) != 1 {
		t.Fatalf("decode elements snapshot: snap=%+v err=%v", snap, err)
	}
	if _, err := toolVal.Run(ctx, map[string]any{"action": "tap", "element_id": 1, "observation": int(snap.Observation)}); err != nil {
		t.Fatalf("tap element failed: %v", err)
	}
	if _, err := toolVal.Run(ctx, map[string]any{"action": "screenshot"}); err != nil {
		t.Fatalf("screenshot failed: %v", err)
	}
	partsTool := toolVal.(*mobileDeviceTool)
	if parts := partsTool.ToolResultParts(""); len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("expected PNG ContentPart from screenshot, got %+v", parts)
	}

	// Non-owner group member MUST NOT get mobile_device!
	memberEvent := MessageEvent{
		Kind:      EventKindGroup,
		Platform:  PlatformOneBotV11,
		ProfileID: cfg.ID,
		GroupID:   "20001",
		UserID:    "10009",
	}
	memberRel := rt.relationshipPolicy(ctx, memberEvent)
	memberReg, err := rt.newReplyAgentRegistry(ctx, cfg, memberEvent, memberRel, nil)
	if err != nil {
		t.Fatalf("newReplyAgentRegistry member: %v", err)
	}
	if _, ok := memberReg.Get(mobileDeviceToolName); ok {
		t.Fatalf("ordinary group member must NOT receive %s tool", mobileDeviceToolName)
	}

	// Safe mode robot MUST NOT get mobile_device even for owner!
	safeCfg := cfg
	safeCfg.AgentMode = AgentModeSafe
	safeReg, err := rt.newReplyAgentRegistry(ctx, safeCfg, ownerEvent, ownerRel, nil)
	if err != nil {
		t.Fatalf("newReplyAgentRegistry safe mode: %v", err)
	}
	if _, ok := safeReg.Get(mobileDeviceToolName); ok {
		t.Fatalf("safe mode must NOT expose %s tool", mobileDeviceToolName)
	}
}

func TestMobileSocialQuietHoursQueueAndStatePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	prevDB := os.Getenv("APP_DB_PATH")
	_ = os.Setenv("APP_DB_PATH", filepath.Join(tmpDir, "test.db"))
	mobileSocialPersistDisabled.Store(false)
	t.Cleanup(func() {
		mobileSocialPersistDisabled.Store(true)
		_ = os.Setenv("APP_DB_PATH", prevDB)
	})

	fakeLLM := &scriptedMobileLLM{
		shareOut: "早啊，昨晚英伟达发了个大新闻，CEO 团队回归了！",
	}
	channel := &recordingChannel{}
	cfg := DefaultBotConfig()
	cfg.ID = "bot-main"
	cfg.Enabled = true
	cfg.Platform = PlatformOneBotV11
	cfg.OwnerID = "10001"
	cfg.Timezone = "UTC"
	cfg.ReplySafetyMasterEnabled = boolPointer(false)

	mgr := NewDefaultPluginManager()
	rawPlugin, _, _ := mgr.PluginForConfiguration(mobileSocialPluginID)
	plugin := rawPlugin.(*MobileSocialPlugin)

	rt := newMobileSocialTestRuntime(cfg, channel, mgr, fakeLLM)
	_, _ = mgr.SetEnabledForProfile(mobileSocialPluginID, cfg.ID, true)
	_, _ = mgr.UpdateSettingsForProfile(mobileSocialPluginID, cfg.ID, map[string]any{
		mobileSocialSettingTargetGroups: "20001",
		mobileSocialSettingQuietStart:   "23:00",
		mobileSocialSettingQuietEnd:     "08:00",
	}, nil)

	// Queue a breaking item at 02:00 UTC (inside quiet hours).
	quietTime := time.Date(2026, 11, 24, 2, 0, 0, 0, time.UTC)
	rt.now = func() time.Time { return quietTime }

	plugin.mu.Lock()
	plugin.state.PendingBreaking = []MobileSocialCandidate{{
		ID:           "nvda-1",
		Source:       mobileSourceX,
		Title:        "英伟达 CEO 团队回归",
		Summary:      "深夜重磅官宣",
		Tier:         "breaking",
		Score:        93,
		CreatedAt:    quietTime.Unix(),
		SharedGroups: map[string]bool{},
	}}
	plugin.saveStateLocked(quietTime)
	plugin.mu.Unlock()

	if pushed := plugin.flushPendingBreaking(context.Background(), SettingValues{}); pushed != 0 {
		t.Fatalf("expected 0 pushes during quiet hours, got %d", pushed)
	}

	// Reload into a fresh plugin instance to verify disk persistence!
	reloaded := NewMobileSocialPlugin(nil)
	reloaded.bindRuntime(rt)
	// Advance clock to 09:05 UTC (quiet hours ended).
	morningTime := time.Date(2026, 11, 24, 9, 5, 0, 0, time.UTC)
	rt.now = func() time.Time { return morningTime }

	if pushed := reloaded.flushPendingBreaking(context.Background(), SettingValues{}); pushed != 1 {
		t.Fatalf("expected queued breaking item pushed after quiet hours on reloaded plugin, got %d", pushed)
	}
}

func TestMobileSocialDemoCatalogsInSync(t *testing.T) {
	promptPath := filepath.Join("..", "..", "frontend", "src", "demo", "demo-prompt-catalog.json")
	promptWant, err := json.MarshalIndent(map[string]any{
		"groups":    PromptGroups(),
		"prompts":   PromptSpecs(),
		"max_runes": PromptOverrideMaxRunes,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	promptWant = append(promptWant, '\n')

	safePath := filepath.Join("..", "..", "frontend", "src", "demo", "demo-agent-safe-mode.json")
	safeWant, err := json.MarshalIndent(AgentSafeModeCatalog(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	safeWant = append(safeWant, '\n')

	if os.Getenv("DIANA_UPDATE_DEMO_CATALOG") == "1" {
		if err := os.WriteFile(promptPath, promptWant, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(safePath, safeWant, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got, err := os.ReadFile(promptPath); err != nil || !bytes.Equal(got, promptWant) {
		t.Fatalf("%s out of sync with PromptSpecs", promptPath)
	}
	if got, err := os.ReadFile(safePath); err != nil || !bytes.Equal(got, safeWant) {
		t.Fatalf("%s out of sync with AgentSafeModeCatalog", safePath)
	}
}

func TestMobileSocialConnectionProbe(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/topstories.json":
			_, _ = w.Write([]byte(`[101]`))
		case "/v0/item/101.json":
			_, _ = w.Write([]byte(`{"id":101,"title":"HN Probe Test","url":"https://example.com/hn","score":210,"descendants":45}`))
		case "/api/topics/hot.json":
			_, _ = w.Write([]byte(`[{"id":202,"title":"V2EX Probe Test","content":"ok","url":"https://www.v2ex.com/t/202","replies":18,"node":{"title":"分享发现"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	fakeDrv := newFakeMobileDriver(t, mobilectl.PlatformIOS)
	fakeDrv.currentApp = "springboard"
	fakeDrv.screensByApp["springboard"] = [][]mobilectl.Element{
		{{Type: "Icon", Label: "小红书", Text: "", X: 40, Y: 180, Width: 60, Height: 60}},
	}

	plugin := NewMobileSocialPlugin(apiSrv.Client())
	plugin.hnBaseURL = apiSrv.URL + "/v0"
	plugin.v2exBaseURL = apiSrv.URL
	plugin.SetControllerForTest(mobilectl.NewControllerWithDriver(mobilectl.Config{Backend: mobilectl.BackendWDA}, fakeDrv))

	report := plugin.TestConnections(context.Background(), SettingValues{
		mobileSocialSettingBackend:        mobilectl.BackendWDA,
		mobileSocialSettingEnabledSources: []string{mobileSourceX, mobileSourceHackerNews, mobileSourceV2EX},
	})
	if !report.OK || !report.MobileOK || len(report.Devices) != 1 || !report.Devices[0].ScreenOK {
		t.Fatalf("expected healthy connection report, got %+v", report)
	}
	if len(report.APISources) != 2 || !report.APISources[0].OK || !report.APISources[1].OK {
		t.Fatalf("expected both HN and V2EX API probes OK, got %+v", report.APISources)
	}
}

func TestMobileSocialStateSnapshotDeleteAndStructuredTargets(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/topstories.json":
			_, _ = w.Write([]byte(`[301]`))
		case "/v0/item/301.json":
			_, _ = w.Write([]byte(`{"id":301,"title":"Major GPU Release","url":"https://example.com/gpu","score":520,"descendants":140}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	itemHash := mobileSocialItemHash(mobileSourceHackerNews, "Major GPU Release", "https://example.com/gpu")
	fakeLLM := &scriptedMobileLLM{
		curateOut: `{"items":[{"hash":"` + itemHash + `","tier":"breaking","score":92,"title":"新一代旗舰显卡正式发布","summary":"架构能效大幅提升","tags":["显卡","硬件"],"angle":"刚刷到新卡发布了"}]}`,
		shareOut:  "刚刷到新一代旗舰显卡正式发布了，参数还挺猛的！",
	}
	channel := &recordingChannel{}
	cfg := DefaultBotConfig()
	cfg.ID = "bot-main"
	cfg.Enabled = true
	cfg.Platform = PlatformOneBotV11
	cfg.OwnerID = "10001"
	cfg.Timezone = "UTC"
	cfg.ReplySafetyMasterEnabled = boolPointer(false)

	mgr := NewDefaultPluginManager()
	rawPlugin, _, _ := mgr.PluginForConfiguration(mobileSocialPluginID)
	plugin := rawPlugin.(*MobileSocialPlugin)
	plugin.httpClient = apiSrv.Client()
	plugin.hnBaseURL = apiSrv.URL + "/v0"

	rt := newMobileSocialTestRuntime(cfg, channel, mgr, fakeLLM)
	rt.now = func() time.Time { return time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC) }
	_, _ = mgr.SetEnabledForProfile(mobileSocialPluginID, cfg.ID, true)

	res, err := plugin.PatrolOnceWithSettings(context.Background(), SettingValues{
		mobileSocialSettingEnabledSources: []string{mobileSourceHackerNews},
		mobileSocialSettingTargetGroups:   "bot-main:group:20001\nbot-main:private:10001",
		mobileSocialSettingQuietStart:     "23:00",
		mobileSocialSettingQuietEnd:       "08:00",
	})
	if err != nil {
		t.Fatalf("PatrolOnceWithSettings failed: %v", err)
	}
	if res.BreakingAdded != 1 || res.PushedGroups != 2 {
		t.Fatalf("expected 1 breaking item pushed to group+private (2 targets), got %+v", res)
	}

	snap := plugin.StateSnapshot()
	if len(snap.Candidates) != 1 || snap.LastSummary == "" {
		t.Fatalf("unexpected StateSnapshot: %+v", snap)
	}
	cid := snap.Candidates[0].ID
	if !plugin.DeleteCandidate(cid) {
		t.Fatalf("expected DeleteCandidate(%s) to return true", cid)
	}
	if plugin.DeleteCandidate(cid) {
		t.Fatalf("expected second DeleteCandidate(%s) to return false", cid)
	}
	if after := plugin.StateSnapshot(); len(after.Candidates) != 0 {
		t.Fatalf("expected empty candidates after delete, got %+v", after.Candidates)
	}
}
