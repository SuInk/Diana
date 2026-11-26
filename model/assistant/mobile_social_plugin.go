// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuInk/diana/model/mobilectl"
)

const (
	// MobileSocialPluginID is the built-in plugin ID for mobile/VM control and social feed patrol.
	MobileSocialPluginID = "official.mobile-social"
	mobileSocialPluginID = MobileSocialPluginID

	mobileSocialSettingBackend          = "driver_backend"
	mobileSocialSettingDeviceID         = "device_id"
	mobileSocialSettingVMName           = "vm_name"
	mobileSocialSettingVMHeadless       = "vm_headless"
	mobileSocialSettingADBAddress       = "adb_address"
	mobileSocialSettingUnlockPIN        = "unlock_pin"
	mobileSocialSettingStayAwakeUSB     = "stay_awake_usb"
	mobileSocialSettingWDAURL           = "wda_url"
	mobileSocialSettingMCPCommand       = "mcp_command"
	mobileSocialSettingAllowAgent       = "allow_agent_control"
	mobileSocialSettingPatrolEnabled    = "patrol_enabled"
	mobileSocialSettingEnabledSources   = "enabled_sources"
	mobileSocialSettingPollMinutes      = "poll_interval_minutes"
	mobileSocialSettingSwipeScreens     = "swipe_screens"
	mobileSocialSettingVisionFallback   = "vision_fallback"
	mobileSocialSettingInterestTopics   = "interest_topics"
	mobileSocialSettingJudgeModel       = "judge_model"
	mobileSocialSettingTargetGroups     = "target_groups"
	mobileSocialSettingBreakingPush     = "breaking_push"
	mobileSocialSettingCasualPool       = "casual_pool"
	mobileSocialSettingBreakingCooldown = "breaking_cooldown_minutes"
	mobileSocialSettingDailyPushLimit   = "daily_push_limit"
	mobileSocialSettingQuietStart       = "quiet_start"
	mobileSocialSettingQuietEnd         = "quiet_end"

	mobileSourceX           = "x"
	mobileSourceXiaohongshu = "xiaohongshu"
	mobileSourceHeybox      = "heybox"
	mobileSourceHackerNews  = "hackernews"
	mobileSourceV2EX        = "v2ex"

	defaultMobileSocialTopics = "AI 与大模型前沿、英伟达/苹果/科技巨头重要动态（如高管回归或变动、重磅新品发布）、Steam 与主机游戏大作资讯、高热数码硬件与有讨论价值的极客趣闻；过滤广告带货、引战骂战、纯情绪宣泄与低质水帖。"

	mobileSocialCandidateTTL   = 30 * time.Hour
	mobileSocialBreakingMaxAge = 16 * time.Hour
	mobileSocialSeenTTL        = 7 * 24 * time.Hour
	mobileSocialMaxCandidates  = 48
	mobileSocialMaxSeen        = 1200
)

// mobileSocialPersistDisabled disables disk persistence across package unit tests in main_test.go.
var mobileSocialPersistDisabled atomic.Bool

// MobileAppSpec maps a logical app alias ("x", "xiaohongshu", "heybox") to Android/iOS identifiers.
type MobileAppSpec struct {
	ID             string
	Label          string
	AndroidPackage string
	IOSBundleID    string
	FallbackURL    string
}

var builtinMobileApps = map[string]MobileAppSpec{
	mobileSourceX: {
		ID:             mobileSourceX,
		Label:          "X (Twitter)",
		AndroidPackage: "com.twitter.android",
		IOSBundleID:    "com.atebits.Tweetie2",
		FallbackURL:    "https://x.com/home",
	},
	mobileSourceXiaohongshu: {
		ID:             mobileSourceXiaohongshu,
		Label:          "小红书",
		AndroidPackage: "com.xingin.xhs",
		IOSBundleID:    "com.xingin.discover",
		FallbackURL:    "https://www.xiaohongshu.com/explore",
	},
	mobileSourceHeybox: {
		ID:             mobileSourceHeybox,
		Label:          "小黑盒",
		AndroidPackage: "com.max.xiaoheihe",
		IOSBundleID:    "com.max.xiaoheihe",
		FallbackURL:    "https://www.xiaoheihe.cn/app/bbs/home",
	},
}

// ResolveMobileAppIdentifier resolves short aliases ("x", "xiaohongshu", "heybox") or passes raw package/bundle IDs through.
func ResolveMobileAppIdentifier(platform, nameOrID string) (appID string, fallbackURL string) {
	key := strings.ToLower(strings.TrimSpace(nameOrID))
	switch key {
	case "twitter", "x.com":
		key = mobileSourceX
	case "xhs", "小红书", "rednote":
		key = mobileSourceXiaohongshu
	case "xiaoheihe", "小黑盒":
		key = mobileSourceHeybox
	}
	if spec, ok := builtinMobileApps[key]; ok {
		if platform == mobilectl.PlatformIOS {
			return spec.IOSBundleID, spec.FallbackURL
		}
		return spec.AndroidPackage, spec.FallbackURL
	}
	return strings.TrimSpace(nameOrID), ""
}

func mobileSocialSourceLabel(source string) string {
	switch source {
	case mobileSourceX:
		return "X"
	case mobileSourceXiaohongshu:
		return "小红书"
	case mobileSourceHeybox:
		return "小黑盒"
	case mobileSourceHackerNews:
		return "Hacker News"
	case mobileSourceV2EX:
		return "V2EX"
	default:
		return source
	}
}

func isMobileDeviceSource(source string) bool {
	switch source {
	case mobileSourceX, mobileSourceXiaohongshu, mobileSourceHeybox:
		return true
	default:
		return false
	}
}

// MobileSocialCandidate is one curated social/tech item ready for breaking push or group candidate pool.
type MobileSocialCandidate struct {
	ID           string          `json:"id"`
	Source       string          `json:"source"`
	Title        string          `json:"title"`
	Summary      string          `json:"summary"`
	URL          string          `json:"url,omitempty"`
	Tier         string          `json:"tier"` // "breaking" | "casual"
	Score        int             `json:"score"`
	Tags         []string        `json:"tags,omitempty"`
	Angle        string          `json:"angle,omitempty"`
	CreatedAt    int64           `json:"created_at"`
	SharedGroups map[string]bool `json:"shared_groups,omitempty"`
}

type mobileSocialPersistedState struct {
	Seen            map[string]int64        `json:"seen,omitempty"`
	Candidates      []MobileSocialCandidate `json:"candidates,omitempty"`
	PendingBreaking []MobileSocialCandidate `json:"pending_breaking,omitempty"`
	GroupPushes     map[string][]int64      `json:"group_pushes,omitempty"`
}

// MobileSocialPlugin controls Android/iPhone devices and patrols social & tech feeds.
type MobileSocialPlugin struct {
	mu          sync.Mutex
	runtime     *Runtime
	change      PluginStateChange
	driverKey   string
	controller  *mobilectl.Controller
	customCtrl  *mobilectl.Controller
	httpClient  *http.Client
	hnBaseURL   string
	v2exBaseURL string
	settleDelay time.Duration
	wakeCh      chan struct{}

	lastPatrolSummary string
	lastPatrolAt      time.Time
	lastPatrolErr     string

	stateLoaded bool
	state       mobileSocialPersistedState
}

// NewMobileSocialPlugin constructs the built-in mobile control & social patrol plugin.
func NewMobileSocialPlugin(client *http.Client) *MobileSocialPlugin {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &MobileSocialPlugin{
		httpClient:  client,
		hnBaseURL:   "https://hacker-news.firebaseio.com/v0",
		v2exBaseURL: "https://www.v2ex.com",
		settleDelay: 2200 * time.Millisecond,
		wakeCh:      make(chan struct{}, 1),
		state: mobileSocialPersistedState{
			Seen:        map[string]int64{},
			GroupPushes: map[string][]int64{},
		},
	}
}

func (p *MobileSocialPlugin) Manifest() PluginManifest {
	backendCondition := func(values ...string) *PluginSettingCondition {
		return &PluginSettingCondition{Key: mobileSocialSettingBackend, Values: values}
	}
	return PluginManifest{
		ID:              mobileSocialPluginID,
		Name:            "手机控制与热闻巡检",
		Version:         "0.1.0",
		Description:     "连接手机或模拟器，定时巡检 X、小红书、小黑盒、Hacker News 与 V2EX 热闻并分享到群聊；支持主人在聊天中操控设备。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		CanAskAgent:     true,
		ReportsErrors:   true,
		Permissions:     []string{"device:mobile", "network:https", "message:send", "agent:tool"},
		Settings: []PluginSettingSpec{
			{
				Key:        mobileSocialSettingBackend,
				Label:      "连接方式",
				Type:       PluginSettingTypeSelect,
				Default:    mobilectl.BackendAuto,
				GlobalOnly: true,
				Options: []PluginSettingOption{
					{Value: mobilectl.BackendAuto, Label: "自动识别"},
					{Value: mobilectl.BackendADB, Label: "安卓（USB / 无线 / 模拟器）"},
					{Value: mobilectl.BackendWDA, Label: "iPhone（WebDriverAgent）"},
					{Value: mobilectl.BackendSimctl, Label: "macOS iOS 模拟器"},
					{Value: mobilectl.BackendMobileMCP, Label: "Mobile MCP"},
				},
			},
			{
				Key:         mobileSocialSettingDeviceID,
				Label:       "指定设备 ID",
				Description: "多台设备同时在线时填写序列号，单台留空。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
			},
			{
				Key:         mobileSocialSettingVMName,
				Label:       "自动启动模拟器名称",
				Description: "无在线设备时按名称拉起本地模拟器，不用可留空。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
				ShowWhen:    backendCondition(mobilectl.BackendAuto, mobilectl.BackendADB, mobilectl.BackendSimctl),
			},
			{
				Key:        mobileSocialSettingVMHeadless,
				Label:      "后台无窗口启动模拟器",
				Type:       PluginSettingTypeBool,
				Default:    true,
				GlobalOnly: true,
				ShowWhen:   backendCondition(mobilectl.BackendAuto, mobilectl.BackendADB),
			},
			{
				Key:         mobileSocialSettingADBAddress,
				Label:       "无线或容器地址",
				Description: "如 127.0.0.1:5555；USB 直连留空。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
				ShowWhen:    backendCondition(mobilectl.BackendAuto, mobilectl.BackendADB),
			},
			{
				Key:         mobileSocialSettingUnlockPIN,
				Label:       "锁屏密码",
				Description: "锁屏时自动点按数字密码解锁，输错一次即停，保存后不回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				GlobalOnly:  true,
				ShowWhen:    backendCondition(mobilectl.BackendAuto, mobilectl.BackendADB),
			},
			{
				Key:         mobileSocialSettingStayAwakeUSB,
				Label:       "插电保持常亮",
				Description: "USB 供电时不自动锁屏。",
				Type:        PluginSettingTypeBool,
				Default:     false,
				GlobalOnly:  true,
				ShowWhen:    backendCondition(mobilectl.BackendAuto, mobilectl.BackendADB),
			},
			{
				Key:         mobileSocialSettingWDAURL,
				Label:       "WebDriverAgent 地址",
				Description: "如 http://127.0.0.1:8100。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
				ShowWhen:    backendCondition(mobilectl.BackendAuto, mobilectl.BackendWDA, mobilectl.BackendSimctl),
			},
			{
				Key:        mobileSocialSettingMCPCommand,
				Label:      "MCP 启动命令",
				Type:       PluginSettingTypeString,
				Default:    mobilectl.DefaultMobileMCPCommand,
				GlobalOnly: true,
				ShowWhen:   backendCondition(mobilectl.BackendMobileMCP),
			},
			{
				Key:         mobileSocialSettingAllowAgent,
				Label:       "允许主人在聊天中操控手机",
				Description: "仅主人可用，群友无法触发。",
				Type:        PluginSettingTypeBool,
				Default:     true,
				GlobalOnly:  true,
			},
			{
				Key:        mobileSocialSettingPatrolEnabled,
				Label:      "开启定时巡检",
				Type:       PluginSettingTypeBool,
				Default:    true,
				GlobalOnly: true,
			},
			{
				Key:         mobileSocialSettingEnabledSources,
				Label:       "巡检平台",
				Description: "前 3 项打开手机 App 读取；Hacker News 与 V2EX 直接走公开接口。",
				Type:        PluginSettingTypeMultiSelect,
				Default:     []string{mobileSourceX, mobileSourceXiaohongshu, mobileSourceHeybox, mobileSourceHackerNews, mobileSourceV2EX},
				GlobalOnly:  true,
				Options: []PluginSettingOption{
					{Value: mobileSourceX, Label: "X"},
					{Value: mobileSourceXiaohongshu, Label: "小红书"},
					{Value: mobileSourceHeybox, Label: "小黑盒"},
					{Value: mobileSourceHackerNews, Label: "Hacker News"},
					{Value: mobileSourceV2EX, Label: "V2EX"},
				},
			},
			{
				Key:        mobileSocialSettingPollMinutes,
				Label:      "巡检间隔",
				Type:       PluginSettingTypeNumber,
				Default:    30,
				Min:        settingRange(5),
				Max:        settingRange(720),
				Step:       5,
				Unit:       "分钟",
				GlobalOnly: true,
			},
			{
				Key:        mobileSocialSettingSwipeScreens,
				Label:      "每次滑动屏数",
				Type:       PluginSettingTypeNumber,
				Default:    2,
				Min:        settingRange(1),
				Max:        settingRange(5),
				Step:       1,
				Unit:       "屏",
				GlobalOnly: true,
			},
			{
				Key:         mobileSocialSettingVisionFallback,
				Label:       "读不到文字时截图识屏",
				Description: "遇到图片帖或特殊排版时交给视觉模型兜底。",
				Type:        PluginSettingTypeBool,
				Default:     true,
				GlobalOnly:  true,
			},
			{
				Key:         mobileSocialSettingInterestTopics,
				Label:       "关注方向",
				Description: "想看哪些话题、过滤哪些内容；留空使用默认方向。",
				Type:        PluginSettingTypeText,
				Default:     "",
				Fallback:    defaultMobileSocialTopics,
				Rows:        3,
			},
			{
				Key:        mobileSocialSettingJudgeModel,
				Label:      "筛选模型",
				Type:       PluginSettingTypeLLMProfile,
				Default:    "",
				GlobalOnly: true,
			},
			{
				Key:         mobileSocialSettingTargetGroups,
				Label:       "通知目标",
				Description: "留空时默认发往已启用本插件的活跃群。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         mobileSocialSettingBreakingPush,
				Label:       "重磅突发主动发群",
				Description: "高分大新闻直接用机器人语气发到群里。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         mobileSocialSettingCasualPool,
				Label:       "日常热帖留作群聊话题",
				Description: "群友聊到相关内容或冷场时顺带提起。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:     mobileSocialSettingBreakingCooldown,
				Label:   "同群推送冷却",
				Type:    PluginSettingTypeNumber,
				Default: 90,
				Min:     settingRange(10),
				Max:     settingRange(1440),
				Step:    10,
				Unit:    "分钟",
			},
			{
				Key:     mobileSocialSettingDailyPushLimit,
				Label:   "每群每日上限",
				Type:    PluginSettingTypeNumber,
				Default: 3,
				Min:     settingRange(1),
				Max:     settingRange(20),
				Step:    1,
				Unit:    "条",
			},
			{
				Key:     mobileSocialSettingQuietStart,
				Label:   "免打扰开始",
				Type:    PluginSettingTypeString,
				Default: defaultProactiveQuietStart,
			},
			{
				Key:     mobileSocialSettingQuietEnd,
				Label:   "免打扰结束",
				Type:    PluginSettingTypeString,
				Default: defaultProactiveQuietEnd,
			},
		},
	}
}

func (p *MobileSocialPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *MobileSocialPlugin) bindRuntime(runtime *Runtime) {
	p.mu.Lock()
	p.runtime = runtime
	p.ensureStateLoadedLocked()
	p.mu.Unlock()
	p.refreshController()
}

func (p *MobileSocialPlugin) PluginStateChanged(change PluginStateChange) {
	p.mu.Lock()
	p.change = change
	p.ensureStateLoadedLocked()
	p.mu.Unlock()
	p.refreshController()
	p.signalWake()
}

func (p *MobileSocialPlugin) signalWake() {
	select {
	case p.wakeCh <- struct{}{}:
	default:
	}
}

// SetControllerForTest injects a custom mobilectl.Controller for deterministic unit testing.
func (p *MobileSocialPlugin) SetControllerForTest(ctrl *mobilectl.Controller) {
	p.mu.Lock()
	p.customCtrl = ctrl
	p.mu.Unlock()
}

func (p *MobileSocialPlugin) activeController() *mobilectl.Controller {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.customCtrl != nil {
		return p.customCtrl
	}
	return p.controller
}

func (p *MobileSocialPlugin) controllerConfigFromSettings(settings SettingValues) mobilectl.Config {
	return mobilectl.Config{
		Backend:      settings.String(mobileSocialSettingBackend, mobilectl.BackendAuto),
		DeviceID:     settings.String(mobileSocialSettingDeviceID, ""),
		VMName:       settings.String(mobileSocialSettingVMName, ""),
		VMHeadless:   settings.Bool(mobileSocialSettingVMHeadless, true),
		ADBAddress:   settings.String(mobileSocialSettingADBAddress, ""),
		UnlockPIN:    settings.String(mobileSocialSettingUnlockPIN, ""),
		StayAwakeUSB: settings.Bool(mobileSocialSettingStayAwakeUSB, false),
		WDAURL:       settings.String(mobileSocialSettingWDAURL, ""),
		MCPCommand:   settings.String(mobileSocialSettingMCPCommand, mobilectl.DefaultMobileMCPCommand),
		HTTPClient:   p.httpClient,
	}
}

func driverConfigKey(cfg mobilectl.Config) string {
	headlessStr := "0"
	if cfg.VMHeadless {
		headlessStr = "1"
	}
	stayAwakeStr := "0"
	if cfg.StayAwakeUSB {
		stayAwakeStr = "1"
	}
	return strings.Join([]string{cfg.Backend, cfg.DeviceID, cfg.VMName, headlessStr, stayAwakeStr, cfg.ADBAddress, cfg.UnlockPIN, cfg.WDAURL, cfg.MCPCommand}, "\x00")
}

func (p *MobileSocialPlugin) refreshController() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.customCtrl != nil {
		return
	}
	if !p.change.Enabled {
		if p.controller != nil {
			_ = p.controller.Close()
			p.controller = nil
		}
		p.driverKey = ""
		return
	}
	cfg := p.controllerConfigFromSettings(p.change.Settings)
	key := driverConfigKey(cfg)
	if key == p.driverKey && p.controller != nil {
		return
	}
	if p.controller != nil {
		_ = p.controller.Close()
	}
	p.driverKey = key
	p.controller = mobilectl.NewController(cfg)
}

// MobileDeviceProbe describes one probed Android/iOS real device or VM during a connection test.
type MobileDeviceProbe struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	Backend       string `json:"backend,omitempty"`
	Virtual       bool   `json:"virtual,omitempty"`
	Locked        bool   `json:"locked,omitempty"`
	Selected      bool   `json:"selected,omitempty"`
	ScreenOK      bool   `json:"screen_ok"`
	ElementsCount int    `json:"elements_count,omitempty"`
	ScreenDetail  string `json:"screen_detail,omitempty"`
}

// MobileAPISourceProbe describes the live reachability of one public API source (Hacker News / V2EX).
type MobileAPISourceProbe struct {
	Source    string `json:"source"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	ItemCount int    `json:"item_count,omitempty"`
	Message   string `json:"message"`
}

// MobileSocialConnectionReport is returned by POST /api/assistant/plugins/mobile-social/test.
type MobileSocialConnectionReport struct {
	OK            bool                   `json:"ok"`
	Backend       string                 `json:"backend"`
	NeedsMobile   bool                   `json:"needs_mobile"`
	MobileOK      bool                   `json:"mobile_ok"`
	MobileMessage string                 `json:"mobile_message"`
	Devices       []MobileDeviceProbe    `json:"devices,omitempty"`
	APISources    []MobileAPISourceProbe `json:"api_sources,omitempty"`
	RuntimeStatus PluginRuntimeStatus    `json:"runtime_status"`
}

// TestConnections probes the submitted (or saved) mobile/VM driver settings and public API sources without saving.
func (p *MobileSocialPlugin) TestConnections(ctx context.Context, settings SettingValues) MobileSocialConnectionReport {
	cfg := p.controllerConfigFromSettings(settings)
	sources := enabledMobileSocialSources(settings)
	needsMobile := settings.Bool(mobileSocialSettingAllowAgent, true)
	for _, src := range sources {
		if isMobileDeviceSource(src) {
			needsMobile = true
			break
		}
	}

	report := MobileSocialConnectionReport{
		Backend:     cfg.Backend,
		NeedsMobile: needsMobile,
	}

	var wg sync.WaitGroup
	var apiMu sync.Mutex
	apiMap := map[string]MobileAPISourceProbe{}

	for _, spec := range []struct {
		key   string
		label string
		fn    func(context.Context, int) ([]MobileSocialRawItem, error)
	}{
		{mobileSourceHackerNews, "Hacker News", p.collectHackerNews},
		{mobileSourceV2EX, "V2EX 热议", p.collectV2EX},
	} {
		enabled := slices.Contains(sources, spec.key)
		if !enabled {
			apiMap[spec.key] = MobileAPISourceProbe{
				Source:  spec.key,
				Label:   spec.label,
				Enabled: false,
				OK:      true,
				Message: "当前未勾选此信息源",
			}
			continue
		}
		wg.Add(1)
		go func(key, label string, fetch func(context.Context, int) ([]MobileSocialRawItem, error)) {
			defer recoverGoroutinePanic("mobile_social.testAPI")
			defer wg.Done()
			apiCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			started := time.Now()
			items, err := fetch(apiCtx, 3)
			ms := max(int64(1), time.Since(started).Milliseconds())
			probe := MobileAPISourceProbe{
				Source:    key,
				Label:     label,
				Enabled:   true,
				LatencyMS: ms,
			}
			if err != nil {
				probe.Message = "请求失败：" + err.Error()
			} else {
				probe.OK = true
				probe.ItemCount = len(items)
				probe.Message = fmt.Sprintf("连通正常（%d ms，读到 %d 条热榜）", ms, len(items))
			}
			apiMu.Lock()
			apiMap[key] = probe
			apiMu.Unlock()
		}(spec.key, spec.label, spec.fn)
	}

	p.mu.Lock()
	injected := p.customCtrl
	existingCtrl := p.controller
	existingKey := p.driverKey
	candCount := len(p.state.Candidates)
	lastSummary := p.lastPatrolSummary
	lastAt := p.lastPatrolAt
	p.mu.Unlock()

	var ctrl *mobilectl.Controller
	ownCtrl := false
	if injected != nil {
		ctrl = injected
	} else if existingCtrl != nil && existingKey == driverConfigKey(cfg) {
		ctrl = existingCtrl
	} else {
		cfg.Timeout = 16 * time.Second
		ctrl = mobilectl.NewController(cfg)
		ownCtrl = true
	}
	if ownCtrl {
		defer ctrl.Close()
	}

	probeCtx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()

	var deviceLabels []string
	var problems []string

	_ = ctrl.WithExclusiveSession(probeCtx, func(probeCtx context.Context) error {
		devices, listErr := ctrl.ListDevices(probeCtx)
		if listErr != nil || len(devices) == 0 {
			if !needsMobile {
				report.MobileOK = true
				report.MobileMessage = "未启用手机端社媒与手机操控，当前仅使用公开 API 信息源"
				return nil
			}
			msg := "未检测到在线手机或虚拟机"
			if listErr != nil && strings.TrimSpace(listErr.Error()) != "" {
				msg += "：" + listErr.Error()
			} else {
				msg += "（请检查安卓 ADB USB/无线/Redroid 容器连接、iPhone WDA 地址或虚拟机名称）"
			}
			report.MobileOK = false
			report.MobileMessage = msg
			problems = append(problems, msg)
			return nil
		}

		selectedDev, resolveErr := ctrl.ResolveDevice(probeCtx, cfg.DeviceID)
		probes := make([]MobileDeviceProbe, 0, len(devices))
		for _, dev := range devices {
			tag := dev.Platform
			if dev.Virtual {
				tag += " · 虚拟机"
			}
			deviceLabels = append(deviceLabels, fmt.Sprintf("%s (%s)", dev.Name, tag))

			dp := MobileDeviceProbe{
				ID:       dev.ID,
				Name:     dev.Name,
				Platform: dev.Platform,
				Backend:  dev.Backend,
				Virtual:  dev.Virtual,
				Locked:   dev.Locked,
				Selected: resolveErr == nil && dev.ID == selectedDev.ID,
			}
			if dp.Selected {
				if dev.Locked {
					dp.ScreenOK = false
					dp.ScreenDetail = "屏幕已锁定，可点「编辑」填写锁屏密码自动解锁"
				} else if snap, elemErr := ctrl.Elements(probeCtx, dev.ID); elemErr == nil && len(snap.Elements) > 0 {
					dp.ScreenOK = true
					dp.ElementsCount = len(snap.Elements)
					dp.ScreenDetail = fmt.Sprintf("控件树可读（%d 个可见元素）", len(snap.Elements))
				} else if _, pngBytes, shotErr := ctrl.Screenshot(probeCtx, dev.ID); shotErr == nil && len(pngBytes) > 0 {
					dp.ScreenOK = true
					if snap.Width > 0 && snap.Height > 0 {
						dp.ScreenDetail = fmt.Sprintf("原生截屏正常（%d×%d），将走多模态识屏", snap.Width, snap.Height)
					} else {
						dp.ScreenDetail = "原生截屏正常，将走多模态识屏"
					}
				} else {
					errDetail := firstNonEmpty(errString(shotErr), errString(elemErr), "无法读取控件树或截屏")
					dp.ScreenDetail = "读取屏幕失败：" + errDetail
				}
			}
			probes = append(probes, dp)
		}
		report.Devices = probes
		if resolveErr != nil {
			report.MobileOK = false
			report.MobileMessage = resolveErr.Error()
			problems = append(problems, resolveErr.Error())
			return nil
		}
		// Check selected device screen status.
		screenReady := false
		var selectedDetail string
		for _, dp := range probes {
			if dp.Selected {
				screenReady = dp.ScreenOK
				selectedDetail = dp.ScreenDetail
				break
			}
		}
		if screenReady {
			report.MobileOK = true
			report.MobileMessage = fmt.Sprintf("已连通 %s（%s）", selectedDev.Name, selectedDetail)
		} else {
			report.MobileOK = false
			report.MobileMessage = fmt.Sprintf("已发现设备 %s，但%s", selectedDev.Name, selectedDetail)
			problems = append(problems, report.MobileMessage)
		}
		return nil
	})

	wg.Wait()
	report.APISources = []MobileAPISourceProbe{
		apiMap[mobileSourceHackerNews],
		apiMap[mobileSourceV2EX],
	}
	apiAllOK := true
	for _, api := range report.APISources {
		if api.Enabled && !api.OK {
			apiAllOK = false
			problems = append(problems, api.Label+" "+api.Message)
		}
	}
	report.OK = report.MobileOK && apiAllOK

	status := PluginRuntimeStatus{
		Problems: problems,
		Recent:   lastSummary,
	}
	if !lastAt.IsZero() {
		status.RecentAt = lastAt.Unix()
	}
	switch {
	case len(problems) == 0 && len(deviceLabels) > 0:
		status.Summary = fmt.Sprintf("已连接 %s · 候选池 %d 条", strings.Join(deviceLabels, "、"), candCount)
	case len(problems) == 0:
		status.Summary = fmt.Sprintf("运行正常 · 候选池 %d 条", candCount)
	default:
		status.Summary = fmt.Sprintf("有 %d 项提示 · 候选池 %d 条", len(problems), candCount)
	}
	report.RuntimeStatus = status
	return report
}

// MobileSocialStateSnapshot exposes the current candidate pool and patrol history to WebUI's records tab.
type MobileSocialStateSnapshot struct {
	LastSummary     string                  `json:"last_summary,omitempty"`
	LastPatrolAt    int64                   `json:"last_patrol_at,omitempty"`
	LastError       string                  `json:"last_error,omitempty"`
	PendingBreaking []MobileSocialCandidate `json:"pending_breaking"`
	Candidates      []MobileSocialCandidate `json:"candidates"`
}

// StateSnapshot returns a copy of the curated candidate pool and recent patrol summary.
func (p *MobileSocialPlugin) StateSnapshot() MobileSocialStateSnapshot {
	now := time.Now()
	p.mu.Lock()
	if p.runtime != nil {
		now = p.runtime.clock()
	}
	p.ensureStateLoadedLocked()
	p.pruneStateLocked(now)
	out := MobileSocialStateSnapshot{
		LastSummary:     p.lastPatrolSummary,
		LastError:       p.lastPatrolErr,
		PendingBreaking: append([]MobileSocialCandidate(nil), p.state.PendingBreaking...),
		Candidates:      make([]MobileSocialCandidate, 0, len(p.state.Candidates)),
	}
	if !p.lastPatrolAt.IsZero() {
		out.LastPatrolAt = p.lastPatrolAt.Unix()
	}
	// Newest first for UI display.
	for i := len(p.state.Candidates) - 1; i >= 0; i-- {
		out.Candidates = append(out.Candidates, p.state.Candidates[i])
	}
	p.mu.Unlock()
	if out.PendingBreaking == nil {
		out.PendingBreaking = []MobileSocialCandidate{}
	}
	return out
}

// DeleteCandidate removes one item by ID from both the candidate pool and pending breaking queue.
func (p *MobileSocialPlugin) DeleteCandidate(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtime != nil {
		now = p.runtime.clock()
	}
	p.ensureStateLoadedLocked()
	removed := false
	nextCand := p.state.Candidates[:0]
	for _, c := range p.state.Candidates {
		if c.ID == id {
			removed = true
			continue
		}
		nextCand = append(nextCand, c)
	}
	p.state.Candidates = nextCand

	nextBreak := p.state.PendingBreaking[:0]
	for _, c := range p.state.PendingBreaking {
		if c.ID == id {
			removed = true
			continue
		}
		nextBreak = append(nextBreak, c)
	}
	p.state.PendingBreaking = nextBreak
	if removed {
		p.saveStateLocked(now)
	}
	return removed
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

// RuntimeStatus reports connected phone status, active sources, and latest patrol summary in WebUI.
func (p *MobileSocialPlugin) RuntimeStatus(ctx context.Context) (PluginRuntimeStatus, bool) {
	p.mu.Lock()
	enabled := p.change.Enabled
	settings := p.change.Settings
	lastSummary := p.lastPatrolSummary
	lastAt := p.lastPatrolAt
	lastErr := p.lastPatrolErr
	candCount := len(p.state.Candidates)
	p.mu.Unlock()

	if !enabled {
		return PluginRuntimeStatus{}, false
	}
	sources := enabledMobileSocialSources(settings)
	needsMobile := settings.Bool(mobileSocialSettingAllowAgent, true)
	for _, src := range sources {
		if isMobileDeviceSource(src) {
			needsMobile = true
			break
		}
	}

	var problems []string
	var deviceLabels []string
	if needsMobile {
		ctrl := p.activeController()
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		devices, err := ctrl.ListDevices(probeCtx)
		cancel()
		if err != nil || len(devices) == 0 {
			msg := "暂未检测到在线手机或虚拟机（X / 小红书 / 小黑盒端内巡检暂不可用，Hacker News / V2EX 接口巡检不受影响）"
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				msg += "：" + err.Error()
			}
			problems = append(problems, msg)
		} else {
			for _, d := range devices {
				tag := d.Platform
				if d.Virtual {
					tag += " · 虚拟机"
				}
				deviceLabels = append(deviceLabels, fmt.Sprintf("%s (%s)", d.Name, tag))
				if d.Locked {
					problems = append(problems, fmt.Sprintf("%s 屏幕已锁定，可点「编辑」填写锁屏密码自动解锁", d.Name))
				}
			}
		}
	}
	if lastErr != "" {
		problems = append(problems, "最近一次巡检部分出错："+lastErr)
	}

	out := PluginRuntimeStatus{
		Problems: problems,
		Recent:   lastSummary,
	}
	if !lastAt.IsZero() {
		out.RecentAt = lastAt.Unix()
	}
	switch {
	case len(problems) == 0 && len(deviceLabels) > 0:
		out.Summary = fmt.Sprintf("已连接 %s · 候选池 %d 条", strings.Join(deviceLabels, "、"), candCount)
	case len(problems) == 0:
		out.Summary = fmt.Sprintf("运行正常 · 候选池 %d 条", candCount)
	default:
		out.Summary = fmt.Sprintf("有 %d 项提示 · 候选池 %d 条", len(problems), candCount)
	}
	return out, true
}

func enabledMobileSocialSources(settings SettingValues) []string {
	defaultList := []string{mobileSourceX, mobileSourceXiaohongshu, mobileSourceHeybox, mobileSourceHackerNews, mobileSourceV2EX}
	selected := settings.StringSlice(mobileSocialSettingEnabledSources)
	if len(selected) == 0 {
		if _, explicit := settings[mobileSocialSettingEnabledSources]; !explicit {
			selected = defaultList
		}
	}
	var out []string
	for _, item := range selected {
		item = strings.ToLower(strings.TrimSpace(item))
		if slices.Contains(defaultList, item) && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func mobileSocialStatePath() string {
	if mobileSocialPersistDisabled.Load() {
		return ""
	}
	dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH"))
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "mobile-social", "state.json")
}

func (p *MobileSocialPlugin) ensureStateLoadedLocked() {
	if p.stateLoaded {
		return
	}
	p.stateLoaded = true
	if p.state.Seen == nil {
		p.state.Seen = map[string]int64{}
	}
	if p.state.GroupPushes == nil {
		p.state.GroupPushes = map[string][]int64{}
	}
	path := mobileSocialStatePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	var loaded mobileSocialPersistedState
	if json.Unmarshal(raw, &loaded) != nil {
		return
	}
	if loaded.Seen == nil {
		loaded.Seen = map[string]int64{}
	}
	if loaded.GroupPushes == nil {
		loaded.GroupPushes = map[string][]int64{}
	}
	p.state = loaded
}

func (p *MobileSocialPlugin) saveStateLocked(now time.Time) {
	p.pruneStateLocked(now)
	path := mobileSocialStatePath()
	if path == "" {
		return
	}
	raw, err := json.Marshal(p.state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (p *MobileSocialPlugin) pruneStateLocked(now time.Time) {
	seenCutoff := now.Add(-mobileSocialSeenTTL).Unix()
	for k, ts := range p.state.Seen {
		if ts < seenCutoff {
			delete(p.state.Seen, k)
		}
	}
	if len(p.state.Seen) > mobileSocialMaxSeen {
		type pair struct {
			k  string
			ts int64
		}
		all := make([]pair, 0, len(p.state.Seen))
		for k, ts := range p.state.Seen {
			all = append(all, pair{k: k, ts: ts})
		}
		slices.SortFunc(all, func(a, b pair) int {
			if a.ts != b.ts {
				if a.ts < b.ts {
					return -1
				}
				return 1
			}
			return strings.Compare(a.k, b.k)
		})
		for _, item := range all[:len(all)-mobileSocialMaxSeen] {
			delete(p.state.Seen, item.k)
		}
	}

	candCutoff := now.Add(-mobileSocialCandidateTTL).Unix()
	filtered := p.state.Candidates[:0]
	for _, c := range p.state.Candidates {
		if c.CreatedAt >= candCutoff {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) > mobileSocialMaxCandidates {
		filtered = filtered[len(filtered)-mobileSocialMaxCandidates:]
	}
	p.state.Candidates = filtered

	breakCutoff := now.Add(-mobileSocialBreakingMaxAge).Unix()
	pending := p.state.PendingBreaking[:0]
	for _, c := range p.state.PendingBreaking {
		if c.CreatedAt >= breakCutoff {
			pending = append(pending, c)
		}
	}
	p.state.PendingBreaking = pending

	pushCutoff := now.Add(-48 * time.Hour).Unix()
	for groupKey, stamps := range p.state.GroupPushes {
		kept := stamps[:0]
		for _, ts := range stamps {
			if ts >= pushCutoff {
				kept = append(kept, ts)
			}
		}
		if len(kept) == 0 {
			delete(p.state.GroupPushes, groupKey)
		} else {
			p.state.GroupPushes[groupKey] = kept
		}
	}
}

func mobileSocialItemHash(source, title, rawURL string) string {
	normTitle := strings.ToLower(strings.Join(strings.Fields(title), " "))
	normURL := strings.TrimSpace(rawURL)
	base := source + "|" + normURL + "|" + truncateRunes(normTitle, 80)
	if normURL != "" {
		base = source + "|url:" + normURL
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:12])
}
