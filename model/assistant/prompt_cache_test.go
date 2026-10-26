// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 消息时间只写绝对时间，同一条在相邻两轮里必须渲染成同一个字符串（#1140）。
func TestContextMessageTimingIsAbsolute(t *testing.T) {
	got := contextMessageTiming(1000, time.UTC)
	if got != "【消息时间：1970-01-01 00:16:40（UTC+00:00）】" {
		t.Fatalf("contextMessageTiming = %q", got)
	}
}

// 同一条历史在相邻两轮里必须渲染成同一个字符串，否则整段历史无法命中前缀缓存。
func TestHistoryLineIsByteStableAcrossTurns(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "m-1",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "旧问题"}}},
	}
	first := historyPromptTextAt(event, 1100)
	second := historyPromptTextAt(event, 90000)
	if first != second {
		t.Fatalf("history line drifted between turns: %q vs %q", first, second)
	}
	want := "[历史 " + time.Unix(1000, 0).Local().Format("2006-01-02 15:04:05") + "] 轩诺: 旧问题"
	if first != want {
		t.Fatalf("history line = %q, want %q", first, want)
	}
	if strings.Contains(first, "距当前") {
		t.Fatalf("history line must not carry request-relative timing: %q", first)
	}
	cross := event
	cross.crossGroupContext = true
	if got := historyPromptTextAt(cross, 1100); !strings.HasPrefix(got, "[跨群历史 ") {
		t.Fatalf("cross-group history line = %q", got)
	}
	media := agentImageHistoryPromptTextWithDescriptions(MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "m-2",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{}}},
	}, 1100, nil)
	if !strings.HasPrefix(media, "[历史 ") || strings.Contains(media, "距当前") {
		t.Fatalf("media history line = %q", media)
	}
}

// 真机跑一轮 QQ 群聊时发现：探针把所有 system 消息合成一段，于是每条消息都报
// 「system 分叉、可复用前缀 0 字节」，而实际缓存命中率有九成。开头的 system 才是
// 稳定段，靠后的（发言者尾部、实时时钟）本来就每轮变，要按位置进消息序列。
func TestPromptCacheProbeIgnoresExpectedTailChurn(t *testing.T) {
	build := func(clock string) llm.GenerateRequest {
		return llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "稳定人设与规则"},
			{Role: llm.RoleUser, Content: "[历史 2026-09-03 10:00:00] 小林: 旧问题"},
			{Role: llm.RoleAssistant, Content: "旧回复", CacheBreakpoint: true},
			{Role: llm.RoleSystem, Content: "关系等级：熟悉"},
			{Role: llm.RoleSystem, Content: clock},
			{Role: llm.RoleUser, Content: "【当前需要回复的消息】新问题"},
		}}
	}
	first := observePromptCachePayload(build("当前运行时钟：10:00:00"))
	second := observePromptCachePayload(build("当前运行时钟：10:00:31"))
	if first.SystemHash != second.SystemHash {
		t.Fatalf("leading system must stay stable across turns")
	}
	if first.StableBoundary != 1 {
		t.Fatalf("stable boundary = %d, want the marked history message", first.StableBoundary)
	}
	divergence := comparePromptCachePayload(first, second)
	if divergence.Segment != "messages" || divergence.MessageIndex <= first.StableBoundary {
		t.Fatalf("tail churn must be reported after the stable boundary: %#v", divergence)
	}
	if !divergence.Expected(first) {
		t.Fatalf("tail churn must not be logged as a cache problem: %#v", divergence)
	}
	if divergence.ReusablePrefixBytes <= 0 {
		t.Fatalf("reusable prefix must cover the stable head and history: %#v", divergence)
	}

	// 真正的前缀断裂仍然要报：历史被改写时命中率是真的会掉。
	broken := build("当前运行时钟：10:00:31")
	broken.Messages[1].Content = "[历史 2026-09-03 10:00:00] 小林: 改写过的旧问题"
	real := comparePromptCachePayload(first, observePromptCachePayload(broken))
	if real.Segment != "messages" || real.MessageIndex != 0 || real.Expected(first) {
		t.Fatalf("a real history rewrite must still be reported: %#v", real)
	}
	// 头部变了同样要报。
	headChanged := build("当前运行时钟：10:00:31")
	headChanged.Messages[0].Content = "换了人设"
	if got := comparePromptCachePayload(first, observePromptCachePayload(headChanged)); got.Segment != "system" || got.Expected(first) {
		t.Fatalf("a system head change must still be reported: %#v", got)
	}
}

func TestReplyPresentationChangesOnlyPromptTail(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-a", UserID: "alice", RawMessage: "test"}
	policy := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "alice")
	var previous promptCachePayloadObservation
	for index, mode := range []replyDeliveryMode{replyDeliveryAuto, replyDeliverySingle} {
		event.replyDeliveryMode = mode
		head, tail := r.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, policy, true, nil)
		presentation := replyPresentationPrompt(!chatSplitLimitsForEvent(cfg, event).SingleMessage, cfg)
		if presentation == "" || !strings.Contains(tail, presentation) || strings.Contains(head, presentation) {
			t.Fatalf("mode %s presentation must appear only in tail", mode)
		}
		request := llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: head},
			{Role: llm.RoleUser, Content: "stable history", CacheBreakpoint: true},
			{Role: llm.RoleSystem, Content: tail},
			{Role: llm.RoleUser, Content: "current input"},
		}}
		current := observePromptCachePayload(request)
		if index > 0 {
			divergence := comparePromptCachePayload(previous, current)
			if divergence.Clean() || !divergence.Expected(previous) {
				t.Fatalf("delivery change must differ only after history: %#v", divergence)
			}
		}
		previous = current
	}
}

func TestAgentVideoHistoryLineCarriesCachedFrameDescriptions(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "video-message-1",
		Segments: []MessageSegment{
			{Type: "video", Data: map[string]string{"file": "clip.mp4"}},
			{Type: "image", Data: map[string]string{"source_type": "video_frame"}},
		},
	}

	text := agentImageHistoryPromptTextWithDescriptions(event, 1120, []string{"视频关键帧1摘要=桌面上放着一台笔记本电脑"})
	for _, want := range []string{
		"【媒体 message_id=video-message-1：视频×1、视频关键帧×1】",
		"视频关键帧1摘要=桌面上放着一台笔记本电脑",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("video history line missing %q: %q", want, text)
		}
	}
	// 为零的种类和工具调用说明不再逐行重复：那些在 system 头部只说一次。
	for _, stale := range []string{"语音", "文件", "count=", dianaHistoryImagesToolName, "当前未附加"} {
		if strings.Contains(text, stale) {
			t.Fatalf("video history line carries boilerplate %q: %q", stale, text)
		}
	}
}

func TestAgentImageHistoryLineCarriesCachedDescriptions(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "image-message-1",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{}},
		},
	}

	bare := agentImageHistoryPromptTextWithDescriptions(event, 1120, nil)
	if !strings.Contains(bare, "【媒体 message_id=image-message-1：图片×1】") {
		t.Fatalf("bare line = %q", bare)
	}
	if strings.Contains(bare, "报纸与干花") {
		t.Fatalf("bare line should not invent a description: %q", bare)
	}

	described := agentImageHistoryPromptTextWithDescriptions(event, 1120, []string{"图片1摘要=报纸与干花拼贴的二次元人物"})
	if !strings.Contains(described, "图片×1") {
		t.Fatalf("described line lost the image count: %q", described)
	}
	if !strings.Contains(described, "图片1摘要=报纸与干花拼贴的二次元人物") {
		t.Fatalf("described line = %q", described)
	}
}

func TestAgentHistoryLineCarriesVoiceAndSupportedFileDescriptions(t *testing.T) {
	event := MessageEvent{
		MessageID: "media-message-1",
		Segments: []MessageSegment{
			{Type: "record", Data: map[string]string{voiceSTTTranscriptKey: "明天下午三点开会"}},
			{Type: "file", Data: map[string]string{"name": "会议纪要.pdf"}},
			{Type: "file", Data: map[string]string{"name": "数据.csv", "summary": "本月订单共 42 条"}},
		},
	}
	descriptions := historicalNonImageMediaDescriptions(event.Segments)
	text := agentImageHistoryPromptTextWithDescriptions(event, 0, descriptions)
	for _, want := range []string{
		"【媒体 message_id=media-message-1：语音×1、文件×2】",
		"语音1转写=明天下午三点开会",
		"文件1摘要=文件名：会议纪要.pdf；格式：pdf；正文尚未解析",
		"文件2摘要=文件名：数据.csv；格式：csv；内容摘要：本月订单共 42 条",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("history media line missing %q: %q", want, text)
		}
	}
}

func TestEverySupportedFileFormatGetsHistoryMetadataDescription(t *testing.T) {
	for extension := range supportedFileExts {
		name := "fixture" + extension
		lines := historicalNonImageMediaDescriptions([]MessageSegment{{Type: "file", Data: map[string]string{"name": name}}})
		if len(lines) != 1 || !strings.Contains(lines[0], "文件名："+name) || !strings.Contains(lines[0], "正文尚未解析") {
			t.Fatalf("supported file %q description = %#v", extension, lines)
		}
	}
}

func TestAgentImageHistoryPromptNeverSerializesImagePlaceholder(t *testing.T) {
	event := MessageEvent{
		MessageID: "image-with-placeholder",
		UserID:    "user-1",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "说明 [图片] 之后的内容"}},
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,YQ=="}},
		},
		Quoted: &QuotedMessage{
			MessageID: "quoted-image",
			Segments: []MessageSegment{
				{Type: "text", Data: map[string]string{"text": "[图片] 引用说明"}},
				{Type: "image", Data: map[string]string{"url": "data:image/png;base64,Yg=="}},
			},
		},
	}
	text := agentImageHistoryPromptTextWithDescriptions(event, 0, nil)
	if strings.Contains(text, "[图片]") || !strings.Contains(text, "说明") || !strings.Contains(text, "引用说明") {
		t.Fatalf("history image prompt = %q", text)
	}
}

// 工具定义排在请求最前面（DeepSeek 等按前缀命中缓存的供应商把它放在 system 之前），
// 同一个群里的非主人发言者必须拿到逐字相同的一份，否则有授权和没授权的人一交替，
// 整段前缀缓存连同后面的历史一起作废。授权差异只能出现在尾部。
func TestReplyToolDefinitionsStableAcrossGroupSpeakers(t *testing.T) {
	plugins := NewDefaultPluginManager()
	if _, err := plugins.UpdateSettings(repositoryPublishPluginID, map[string]any{
		repositoryPublishSettingAllowlist:    "acme/demo",
		repositoryPublishSettingManagerUsers: "20001 = acme/demo",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := BotConfig{OwnerID: "10001", BotAccount: "10000", AgentEnabled: true, Platform: PlatformOneBotV11}
	r := NewRuntime(cfg, nilChannel{}, plugins, nil, nil, nil, nil)
	definitions := func(user, role string) (string, string) {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: user, SelfID: "10000", SenderRole: role, Platform: PlatformOneBotV11}
		policy := RelationshipPolicyFor(UserMemoryProfile{}, "10001", user)
		registry, err := r.newReplyAgentRegistry(context.Background(), r.ProfileConfig(""), event, policy, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		if _, ok := registry.Get(dianaGitHubToolName); !ok {
			t.Fatalf("user %s: github tool not registered", user)
		}
		raw, err := json.Marshal(registry.Definitions())
		if err != nil {
			t.Fatal(err)
		}
		_, tail := r.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, policy, true, registry)
		return string(raw), tail
	}
	granted, grantedTail := definitions("20001", "admin")
	stranger, strangerTail := definitions("20002", "member")
	if granted != stranger {
		t.Fatalf("tool definitions differ between speakers:\n%s\n%s", granted, stranger)
	}
	if !strings.Contains(grantedTail, "当前会话有写入授权的仓库：acme/demo") {
		t.Fatalf("granted tail misses repository list: %s", grantedTail)
	}
	if strings.Contains(strangerTail, "acme/demo") || !strings.Contains(strangerTail, "当前会话无写入授权") {
		t.Fatalf("stranger tail = %s", strangerTail)
	}
}

// 主人和群友在同一个群里交替说话时，发给模型的工具定义、Agent system 和人设头部
// 必须逐字相同：主人专属工具、预算和规则都在历史之后的尾部（agent.Config.SharedTools）。
func TestReplyPrefixSameForOwnerAndMember(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	workDir := t.TempDir()
	cfg := standardModeBotConfig()
	cfg.BotAccount, cfg.OwnerID = "10000", "10001"
	cfg.GroupTriggers = []string{"Diana"}
	cfg.AgentSkillRoots = []string{filepath.Join(workDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	cfg.AgentCommandAllowlist = []string{"echo"}
	cfg.AgentFileWriteEnabled = true
	cfg.ReplySafetyMasterEnabled = boolPointer(false)
	provider := &agentRequestRecorder{}
	runtime := NewRuntime(cfg, &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	defer runtime.closeAgentRegistryCache()
	prefix := func(user, id string) (string, llm.GenerateRequest) {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: user, SelfID: "10000", MessageID: id, RawMessage: "Diana 你好", Platform: PlatformOneBotV11, Time: time.Now().Unix()}
		if _, err := runtime.replyTo(context.Background(), event, event.RawMessage); err != nil {
			t.Fatal(err)
		}
		req := provider.agent
		if len(req.Tools) == 0 {
			t.Fatalf("user %s: no agent request captured", user)
		}
		var builder strings.Builder
		for _, definition := range req.Tools {
			raw, _ := json.Marshal(definition)
			builder.Write(raw)
			builder.WriteString("\n")
		}
		for _, message := range req.Messages {
			if message.Role != llm.RoleSystem {
				break
			}
			builder.WriteString(message.Content + "\n")
		}
		return builder.String(), req
	}
	member, _ := prefix("20002", "m1")
	owner, ownerReq := prefix("10001", "m2")
	if member != owner {
		a, b := []rune(member), []rune(owner)
		i := 0
		for i < len(a) && i < len(b) && a[i] == b[i] {
			i++
		}
		t.Fatalf("prefix diverges at rune %d:\nmember: %q\nowner:  %q", i, string(a[max(0, i-80):min(len(a), i+160)]), string(b[max(0, i-80):min(len(b), i+160)]))
	}
	foundTail := false
	for _, message := range ownerReq.Messages {
		foundTail = foundTail || strings.Contains(message.Content, "【当前发言者另有的按需工具】")
	}
	if !foundTail {
		var names []string
		for _, d := range ownerReq.Tools {
			names = append(names, d.Name)
		}
		for i, m := range ownerReq.Messages {
			c := []rune(m.Content)
			t.Logf("%d %s %q", i, m.Role, string(c[:min(len(c), 80)]))
		}
		t.Fatalf("owner tail lost the speaker tool section; tools=%v", names)
	}
}

// agentRequestRecorder 记下最近一次带工具定义的请求（Agent 主回复），其余调用一律答「好」。
type agentRequestRecorder struct{ agent llm.GenerateRequest }

func (p *agentRequestRecorder) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if len(req.Tools) > 0 {
		p.agent = req
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"final","content":"好"}`}, nil
}
