package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestRuntimeShouldHandleGroupMentionAndTrigger 验证对应功能场景。
func TestRuntimeShouldHandleGroupMentionAndTrigger(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		GroupTriggers:  []string{"Diana"},
		BotQQ:          "42",
		DisabledGroups: []string{"999"},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	if !runtime.shouldHandle(MessageEvent{Kind: EventKindGroup, ToMe: true}, "hello") {
		t.Fatal("mention should trigger")
	}
	if !runtime.shouldHandle(MessageEvent{Kind: EventKindGroup}, "Diana 帮我看看") {
		t.Fatal("alias should trigger")
	}
	if runtime.shouldHandle(MessageEvent{Kind: EventKindGroup}, "普通群聊") {
		t.Fatal("plain group message should not trigger")
	}
	if !runtime.shouldHandle(MessageEvent{Kind: EventKindPrivate}, "hello") {
		t.Fatal("private message should trigger")
	}
	if runtime.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "999", ToMe: true}, "hello") {
		t.Fatal("disabled group should not trigger")
	}
}

// TestRuntimeSystemPromptMentionsHomophoneJokes 验证系统提示包含中文谐音梗处理要求。
func TestRuntimeSystemPromptMentionsHomophoneJokes(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup}, nil)
	if !strings.Contains(prompt, "谐音梗") || !strings.Contains(prompt, "能接梗就自然接") {
		t.Fatalf("system prompt missing homophone guidance: %q", prompt)
	}
}

// TestRuntimeIgnoresSelfMessage 验证对应功能场景。
func TestRuntimeIgnoresSelfMessage(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		BotQQ: "42",
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if !runtime.isSelfMessage(MessageEvent{UserID: "42"}) {
		t.Fatal("self message should be ignored")
	}
	if runtime.isSelfMessage(MessageEvent{UserID: "10001"}) {
		t.Fatal("other user should not be treated as self")
	}
}

func TestRuntimeLearnsBotQQFromChannelStatus(t *testing.T) {
	saver := &recordingConfigSaver{}
	channel := statusChannel{status: ChannelStatus{Connected: true, SelfID: "1784464"}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, saver, nil)

	status := runtime.Status()

	if status.Config.BotQQ != "1784464" {
		t.Fatalf("BotQQ = %q, want 1784464", status.Config.BotQQ)
	}
	if saver.saved.BotQQ != "1784464" || saver.calls != 1 {
		t.Fatalf("saved config = %#v, calls = %d", saver.saved, saver.calls)
	}
	runtime.Status()
	if saver.calls != 1 {
		t.Fatalf("unchanged identity persisted %d times, want once", saver.calls)
	}
}

func TestRuntimeDoesNotOverwriteConfiguredBotQQ(t *testing.T) {
	saver := &recordingConfigSaver{}
	channel := statusChannel{status: ChannelStatus{Connected: true, SelfID: "1784464"}}
	runtime := NewRuntime(BotConfig{BotQQ: "10001"}, channel, NewPluginManager(), nil, nil, saver, nil)

	status := runtime.Status()

	if status.Config.BotQQ != "10001" {
		t.Fatalf("BotQQ = %q, want existing value", status.Config.BotQQ)
	}
	if saver.calls != 0 {
		t.Fatalf("existing identity persisted %d times", saver.calls)
	}
}

func TestRuntimePrivateMessageInterceptorConsumesBeforeChat(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	called := false
	runtime.SetPrivateMessageInterceptor(func(_ context.Context, event MessageEvent, text string) bool {
		called = event.Kind == EventKindPrivate && text == "123456"
		return called
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", RawMessage: "123456"}

	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if !called {
		t.Fatal("private message interceptor was not called")
	}
	if history := runtime.contextHistory(event); len(history) != 0 {
		t.Fatalf("consumed login message entered chat history: %#v", history)
	}
	recent := runtime.Status().RecentEvents
	if len(recent) != 1 || recent[0].Text != "[控制台登录配对]" || !recent[0].Handled {
		t.Fatalf("recent events = %#v", recent)
	}
}

// TestSplitReplyHonorsBotbrAndChunkSize 验证对应功能场景。
func TestSplitReplyHonorsBotbrAndChunkSize(t *testing.T) {
	got := splitReply("abc<botbr>defgh", 3)
	want := []string{"abc", "def", "gh"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRuntimeOwnerCommandsSwitchProfilesAndClearHistory 验证对应功能场景。
func TestRuntimeOwnerCommandsSwitchProfilesAndClearHistory(t *testing.T) {
	reminders := &stubReminderStore{}
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "a",
			Profiles: []llm.Profile{
				{ID: "a", Name: "主配置", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "gp5.5"}},
				{ID: "b", Name: "备用配置", Config: llm.ProviderConfig{Provider: llm.ProviderAnthropic, Model: "claude-sonnet-4-5"}},
			},
		},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, reminders, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	reply, handled := runtime.handleOwnerCommand(event, "lllm 当前")
	if !handled || reply == "" || !strings.Contains(reply, "主配置") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}

	reply, handled = runtime.handleOwnerCommand(event, "lllm 切换 备用配置")
	if !handled || !strings.Contains(reply, "备用配置") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if store.set.ActiveID != "b" {
		t.Fatalf("ActiveID = %q, want b", store.set.ActiveID)
	}

	runtime.history[sessionKey(event)] = []historyEntry{{event: MessageEvent{MessageID: "1"}}}
	reply, handled = runtime.handleOwnerCommand(event, "清空上下文")
	if !handled || !strings.Contains(reply, "已清空") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if history := runtime.contextHistory(event); len(history) != 0 {
		t.Fatalf("history = %#v", history)
	}

	reply, handled = runtime.handleOwnerCommand(event, "提醒 添加 1m 记得喝水")
	if !handled || !strings.Contains(reply, "提醒已创建") || len(reminders.items) != 1 {
		t.Fatalf("reply=%q handled=%v reminders=%#v", reply, handled, reminders.items)
	}

	reply, handled = runtime.handleOwnerCommand(event, "提醒 删除 "+reminders.items[0].ID)
	if !handled || !strings.Contains(reply, "提醒已删除") || len(reminders.items) != 0 {
		t.Fatalf("reply=%q handled=%v reminders=%#v", reply, handled, reminders.items)
	}

	reply, handled = runtime.handleOwnerCommand(event, "群 禁用 123456")
	if !handled || !strings.Contains(reply, "已禁用") || !runtime.isGroupDisabled("123456") {
		t.Fatalf("reply=%q handled=%v disabled=%v", reply, handled, runtime.isGroupDisabled("123456"))
	}

	reply, handled = runtime.handleOwnerCommand(event, "群 启用 123456")
	if !handled || !strings.Contains(reply, "已恢复") || runtime.isGroupDisabled("123456") {
		t.Fatalf("reply=%q handled=%v disabled=%v", reply, handled, runtime.isGroupDisabled("123456"))
	}
}

// TestRuntimeLLMConfigSkillRepliesBeforeLLM 验证对应功能场景。
func TestRuntimeLLMConfigSkillRepliesBeforeLLM(t *testing.T) {
	channel := &recordingChannel{}
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
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewDefaultPluginManager(), store, nil, nil, func() (LLMProvider, error) {
		t.Fatal("llmFactory should not be called for config skill command")
		return nil, nil
	})

	reply, err := runtime.replyTo(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "10001", RawMessage: "以后用 Anthropic 的 claude-sonnet-4-5"}, "以后用 Anthropic 的 claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	if !strings.Contains(reply, "已更新当前 LLM") || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if got := store.Current(); got.Provider != llm.ProviderAnthropic || got.Model != "claude-sonnet-4-5" {
		t.Fatalf("current = %#v", got)
	}
}

// TestRuntimeKeepsMentionAndGroupTriggerInPrompt 验证群 @ 和触发词会保留给模型，而不是被剥成空输入。
func TestRuntimeKeepsMentionAndGroupTriggerInPrompt(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "在呢"}
	runtime := NewRuntime(BotConfig{
		BotQQ:         "42",
		GroupTriggers: []string{"嘉然"},
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	reply, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     "10001",
		MessageID:  "msg-1",
		RawMessage: "[CQ:at,qq=42] 嘉然",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " 嘉然"}},
		},
	}, "@42 嘉然")
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	if reply != provider.reply || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	got := provider.request.Messages[len(provider.request.Messages)-1].Content
	if got != "@42 嘉然" {
		t.Fatalf("last message content = %q, want @42 嘉然", got)
	}
}

// TestRuntimeMentionOnlyUsesFallbackPrompt 验证只艾特机器人时不会向 LLM 传空消息。
func TestRuntimeMentionOnlyUsesFallbackPrompt(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "在"}
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	_, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     "10001",
		MessageID:  "msg-1",
		RawMessage: "[CQ:at,qq=42]",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
		},
	}, "@42")
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	got := provider.request.Messages[len(provider.request.Messages)-1].Content
	if got == "" {
		t.Fatal("last message content should not be empty")
	}
}

// TestRuntimeCarriesRecentImageIntoFollowup 验证图片消息后的追问会把历史图片带进 LLM。
func TestRuntimeCarriesRecentImageIntoFollowup(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "这是一张测试图片。"}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "10001",
		MessageID: "img-1",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "https://example.com/image.jpg"}},
		},
		SenderName: "Diana",
	})

	reply, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "10001",
		MessageID:  "q-1",
		RawMessage: "这是什么",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "这是什么"}},
		},
	}, "这是什么")
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	if reply != provider.reply || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if !requestHasImageURL(provider.request, "https://example.com/image.jpg") {
		t.Fatalf("request missing image url: %#v", provider.request.Messages)
	}
}

// TestRuntimeFailsOverLLMProfilesWithinGroup 验证账号失效时只在当前分组内轮换到下一个配置。
func TestRuntimeFailsOverLLMProfilesWithinGroup(t *testing.T) {
	channel := &recordingChannel{}
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "a",
			Profiles: []llm.Profile{
				{ID: "a", Name: "账号 1", Group: "chat", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-a", Model: "bad-model"}},
				{ID: "b", Name: "账号 2", Group: "chat", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-b", Model: "good-model"}},
				{ID: "c", Name: "视觉账号", Group: "vision", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-c", Model: "vision-model"}},
			},
		},
	}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), store, nil, nil, nil)
	var attempts []string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, cfg.Model)
		if cfg.Model == "bad-model" {
			return failingLLMProvider{err: errors.New("401 Unauthorized: invalid api key")}, nil
		}
		return &capturingLLMProvider{reply: "备用账号已接管"}, nil
	})

	reply, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "10001",
		MessageID: "q-1",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "你好"}}},
	}, "你好")
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	if reply != "备用账号已接管" || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if store.set.ActiveID != "b" {
		t.Fatalf("ActiveID = %q, want b", store.set.ActiveID)
	}
	wantAttempts := []string{"bad-model", "good-model"}
	if len(attempts) != len(wantAttempts) {
		t.Fatalf("attempts = %#v, want %#v", attempts, wantAttempts)
	}
	for i := range wantAttempts {
		if attempts[i] != wantAttempts[i] {
			t.Fatalf("attempts = %#v, want %#v", attempts, wantAttempts)
		}
	}
}

// TestRuntimeSendsWelcomeOnGroupIncrease 验证对应功能场景。
func TestRuntimeSendsWelcomeOnGroupIncrease(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMessage: "欢迎 {user_id}",
	}, channel, NewPluginManager(), nil, nil, nil, nil)

	err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:    EventKindNotice,
		SubType: "group_increase",
		GroupID: "123456",
		UserID:  "10001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].GroupID != "123456" || channel.sent[0].MentionUserID != "10001" || !strings.Contains(channel.sent[0].Text, "10001") {
		t.Fatalf("sent = %#v", channel.sent[0])
	}
}

type scriptedProvider struct {
	reply string
}

// Generate 返回预置回复。
func (p scriptedProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{Text: p.reply}, nil
}

// TestGenerateReplyRoutesImagesToVisionGroup 验证对应功能场景。
func TestGenerateReplyRoutesImagesToVisionGroup(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "chat",
			Profiles: []llm.Profile{
				{ID: "chat", Name: "对话", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-chat", Model: "chat-model"}},
				{ID: "vis", Name: "识图", Group: "vision", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-vis", Model: "vision-model"}},
			},
		},
	}
	var usedModels []string
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		usedModels = append(usedModels, cfg.Model)
		return scriptedProvider{reply: "ok from " + cfg.Model}, nil
	})

	// 带图片的消息走 vision 组。
	imageMessages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://img/1.png"}}}}
	reply, err := runtime.generateReply(context.Background(), BotConfig{}.WithDefaults(), imageMessages)
	if err != nil {
		t.Fatalf("generateReply(image) error = %v", err)
	}
	if reply != "ok from vision-model" {
		t.Fatalf("image reply = %q", reply)
	}
	// 纯文本消息走对话组。
	textMessages := []llm.Message{{Role: llm.RoleUser, Content: "你好"}}
	reply, err = runtime.generateReply(context.Background(), BotConfig{}.WithDefaults(), textMessages)
	if err != nil {
		t.Fatalf("generateReply(text) error = %v", err)
	}
	if reply != "ok from chat-model" {
		t.Fatalf("text reply = %q", reply)
	}
	if usedModels[0] != "vision-model" || usedModels[1] != "chat-model" {
		t.Fatalf("usedModels = %v", usedModels)
	}
	// 专用组成功不应改变激活项。
	if store.set.ActiveID != "chat" {
		t.Fatalf("ActiveID changed to %q", store.set.ActiveID)
	}
}

// TestModelRolesBindingTakesPriority 验证对应功能场景。
func TestModelRolesBindingTakesPriority(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "chan-a",
			Profiles: []llm.Profile{
				{ID: "chan-a", Name: "渠道A", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-a", Model: "old-default"}},
				{ID: "chan-b", Name: "渠道B", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-b"}},
			},
		},
	}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "chan-a", Model: "gpt-chat"},
		"vision": {ProfileID: "chan-b", Model: "gpt-vision"},
	}}
	var used []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(pc llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, pc.Model+"@"+pc.APIKey)
		return scriptedProvider{reply: "ok"}, nil
	})

	// 文本消息用 chat 绑定（渠道A + gpt-chat，覆盖渠道默认模型）。
	if _, err := runtime.generateReply(context.Background(), cfg.WithDefaults(), []llm.Message{{Role: llm.RoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("chat generateReply error = %v", err)
	}
	// 图片消息用 vision 绑定（渠道B，渠道本身无默认模型也可用）。
	imageMessages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://img/1"}}}}
	if _, err := runtime.generateReply(context.Background(), cfg.WithDefaults(), imageMessages); err != nil {
		t.Fatalf("vision generateReply error = %v", err)
	}
	if len(used) != 2 || used[0] != "gpt-chat@key-a" || used[1] != "gpt-vision@key-b" {
		t.Fatalf("used = %v", used)
	}

	// 只绑定 chat 时，图片消息回退 chat 绑定。
	used = nil
	runtime2 := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"chat": {ProfileID: "chan-b", Model: "only-chat"}}}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime2.SetLLMProviderConfigFactory(func(pc llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, pc.Model)
		return scriptedProvider{reply: "ok"}, nil
	})
	if _, err := runtime2.generateReply(context.Background(), BotConfig{}.WithDefaults(), imageMessages); err != nil {
		t.Fatalf("fallback generateReply error = %v", err)
	}
	if len(used) != 1 || used[0] != "only-chat" {
		t.Fatalf("fallback used = %v", used)
	}
}

// TestModelRoleGroupBindingRotates 验证对应功能场景。
func TestModelRoleGroupBindingRotates(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "k1",
			Profiles: []llm.Profile{
				{ID: "k1", Name: "主力1", Group: "主力", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-1"}},
				{ID: "k2", Name: "主力2", Group: "主力", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-2"}},
			},
		},
	}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{
		"chat": {Group: "主力", Model: "gpt-x"},
	}}
	var attempts []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(pc llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, pc.APIKey+"/"+pc.Model)
		if pc.APIKey == "key-1" {
			// 第一个渠道限流，应轮换到第二个。
			return failingLLMProvider{err: fmt.Errorf("429 rate limit")}, nil
		}
		return scriptedProvider{reply: "ok"}, nil
	})

	reply, err := runtime.generateReply(context.Background(), cfg.WithDefaults(), []llm.Message{{Role: llm.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("generateReply() error = %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q", reply)
	}
	if len(attempts) != 2 || attempts[0] != "key-1/gpt-x" || attempts[1] != "key-2/gpt-x" {
		t.Fatalf("attempts = %v", attempts)
	}
	// 分组轮换不应改动 LLM 页的激活游标。
	if store.set.ActiveID != "k1" {
		t.Fatalf("ActiveID = %q", store.set.ActiveID)
	}
}

// TestGenerateReplyVisionFallsBackWithoutGroup 验证对应功能场景。
func TestGenerateReplyVisionFallsBackWithoutGroup(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			ActiveID: "chat",
			Profiles: []llm.Profile{
				{ID: "chat", Name: "对话", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key", Model: "chat-model"}},
			},
		},
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		return scriptedProvider{reply: "ok from " + cfg.Model}, nil
	})
	imageMessages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://img/1.png"}}}}
	reply, err := runtime.generateReply(context.Background(), BotConfig{}.WithDefaults(), imageMessages)
	if err != nil {
		t.Fatalf("generateReply() error = %v", err)
	}
	if reply != "ok from chat-model" {
		t.Fatalf("fallback reply = %q", reply)
	}
}

type stubLLMProfileStore struct {
	set llm.ProfileSet
}

// Current 封装当前模块的 Current 逻辑。
func (s *stubLLMProfileStore) Current() llm.ProviderConfig {
	profile, _ := s.set.Current()
	return profile.Config
}

// Profiles 封装当前模块的 Profiles 逻辑。
func (s *stubLLMProfileStore) Profiles() llm.ProfileSet {
	return s.set
}

// SaveProfiles 保存Profiles数据。
func (s *stubLLMProfileStore) SaveProfiles(set llm.ProfileSet) {
	s.set = set
}

type stubReminderStore struct {
	items []Reminder
}

// Reminders 封装当前模块的 Reminders 逻辑。
func (s *stubReminderStore) Reminders() []Reminder {
	return append([]Reminder(nil), s.items...)
}

// SaveReminders 保存Reminders数据。
func (s *stubReminderStore) SaveReminders(items []Reminder) {
	s.items = append([]Reminder(nil), items...)
}

type nilChannel struct{}

// Connect 封装当前模块的 Connect 逻辑。
func (nilChannel) Connect(ctx context.Context, handler EventHandler) error { return nil }

// Send 封装当前模块的 Send 逻辑。
func (nilChannel) Send(ctx context.Context, msg OutgoingMessage) error { return nil }

// CallAPI 封装当前模块的 CallAPI 逻辑。
func (nilChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	return nil, nil
}

// Status 返回当前状态快照。
func (nilChannel) Status() ChannelStatus { return ChannelStatus{} }

type statusChannel struct {
	nilChannel
	status ChannelStatus
}

func (c statusChannel) Status() ChannelStatus { return c.status }

type recordingConfigSaver struct {
	saved BotConfig
	calls int
}

func (s *recordingConfigSaver) SaveBotConfig(cfg BotConfig) {
	s.saved = cfg
	s.calls++
}

// Close 释放当前对象持有的资源。
func (nilChannel) Close() error { return nil }

type recordingChannel struct {
	sent []OutgoingMessage
}

// Connect 封装当前模块的 Connect 逻辑。
func (c *recordingChannel) Connect(ctx context.Context, handler EventHandler) error { return nil }

// Send 封装当前模块的 Send 逻辑。
func (c *recordingChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	c.sent = append(c.sent, msg)
	return nil
}

// CallAPI 封装当前模块的 CallAPI 逻辑。
func (c *recordingChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	return nil, nil
}

// Status 返回当前状态快照。
func (c *recordingChannel) Status() ChannelStatus { return ChannelStatus{} }

// Close 释放当前对象持有的资源。
func (c *recordingChannel) Close() error { return nil }

type capturingLLMProvider struct {
	reply   string
	request llm.GenerateRequest
}

// Generate 记录请求并返回固定回复。
func (p *capturingLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.request = req
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

type failingLLMProvider struct {
	err error
}

// Generate 返回预设错误。
func (p failingLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return nil, p.err
}

func requestHasImageURL(req llm.GenerateRequest, imageURL string) bool {
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL == imageURL {
				return true
			}
		}
	}
	return false
}
