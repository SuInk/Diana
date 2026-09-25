// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func textEvent(messageID, userID, text string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, MessageID: messageID,
		Time: at, RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func stickerEvent(messageID, userID string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, MessageID: messageID, Time: at,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": messageID + ".gif", "sub_type": "1", "url": "http://x/" + messageID + ".gif"}}},
	}
}

func photoEvent(messageID, userID string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, MessageID: messageID, Time: at,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": messageID + ".jpg", "url": "http://x/" + messageID + ".jpg"}}},
	}
}

func dependencyIDs(images []senderDependencyImage) string {
	ids := make([]string, 0, len(images))
	for _, image := range images {
		ids = append(ids, image.Source.MessageID+"#"+fmt.Sprint(image.SegmentIndex))
	}
	return strings.Join(ids, ",")
}

// 先发图、再发字，两条各是各的：图那条不会被注销、也不会塞进文字那条；
// 同一个人的两条仍进同一个主动接话批次，由批次一起看。
func TestMediaOnlyThenTextFromSameSenderStaysTwoMessages(t *testing.T) {
	store := newMemoryInboundEventStore()
	runtime := NewRuntime(replyClosedConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: `{}`}, nil
	})
	runtime.SetInboundEventStore(store)
	photo := photoEvent("photo-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1014)
	for _, event := range []MessageEvent{photo, question} {
		if _, _, err := store.EnqueueInboundEvent(context.Background(), "group:123456", event); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []MessageEvent{photo, question} {
		item := InboundQueueItem{ID: "group:123456:" + event.MessageID, Session: "group:123456", Event: event}
		if _, err := runtime.processInboundQueueItem(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []MessageEvent{photo, question} {
		if turn, superseded, _ := store.InboundEventSuperseded(context.Background(), event); superseded {
			t.Fatalf("%s was superseded by %s", event.MessageID, turn)
		}
	}
	for _, item := range runtime.contextHistory(question) {
		if item.MessageID == "q-1" && (len(item.Segments) != 1 || item.Segments[0].Type != "text") {
			t.Fatalf("text message got media merged into it: %#v", item.Segments)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.mu.Lock()
	runtime.running, runtime.runCtx = true, ctx
	runtime.mu.Unlock()
	if !runtime.enqueueProactiveReply(photo, "") || !runtime.enqueueProactiveReply(question, "这是什么") {
		t.Fatal("proactive batch refused candidates")
	}
	runtime.proactiveBatchMu.Lock()
	batch := runtime.proactiveBatches[proactiveReplyBatchKey(question)]
	count := 0
	if batch != nil {
		count = len(batch.items)
	}
	runtime.proactiveBatchMu.Unlock()
	if count != 2 {
		t.Fatalf("proactive batch should see both messages, got %d", count)
	}
}

func TestSenderDependencyImagesSelection(t *testing.T) {
	const now = 10_000
	question := textEvent("q", "irony", "能不能把 winter 姐姐头像变成机器人风格", now)

	t.Run("sticker skipped", func(t *testing.T) {
		history := []MessageEvent{stickerEvent("sticker", "irony", now-14), question}
		if got := senderDependencyImages(history, question, nil, "bot"); len(got) != 0 {
			t.Fatalf("sticker became a dependency: %s", dependencyIDs(got))
		}
	})
	t.Run("summary sticker and spam skipped", func(t *testing.T) {
		summary := photoEvent("summary", "irony", now-20)
		summary.Segments[0].Data["summary"] = "[动画表情]"
		spamA := photoEvent("spam-a", "someone", now-40)
		spamA.Segments[0].Data["file"] = "same.jpg"
		spamB := photoEvent("spam-b", "irony", now-10)
		spamB.Segments[0].Data["file"] = "same.jpg"
		history := []MessageEvent{spamA, summary, spamB, question}
		if got := senderDependencyImages(history, question, nil, "bot"); len(got) != 0 {
			t.Fatalf("sticker-like images became dependencies: %s", dependencyIDs(got))
		}
	})
	t.Run("cap four newest first", func(t *testing.T) {
		older := photoEvent("older", "irony", now-60)
		older.Segments = append(older.Segments, older.Segments[0], older.Segments[0])
		older.Segments[1] = MessageSegment{Type: "image", Data: map[string]string{"file": "o2.jpg", "url": "http://x/o2.jpg"}}
		older.Segments[2] = MessageSegment{Type: "image", Data: map[string]string{"file": "o3.jpg", "url": "http://x/o3.jpg"}}
		newer := photoEvent("newer", "irony", now-30)
		newer.Segments = append(newer.Segments, MessageSegment{Type: "image", Data: map[string]string{"file": "n2.jpg", "url": "http://x/n2.jpg"}})
		history := []MessageEvent{older, newer, question}
		got := senderDependencyImages(history, question, nil, "bot")
		if dependencyIDs(got) != "older#1,older#2,newer#0,newer#1" {
			t.Fatalf("dependencies = %s", dependencyIDs(got))
		}
	})
	t.Run("at most two messages within three minutes", func(t *testing.T) {
		history := []MessageEvent{
			photoEvent("too-old", "irony", now-600),
			photoEvent("a", "irony", now-100),
			photoEvent("b", "irony", now-50),
			photoEvent("c", "irony", now-20),
			question,
		}
		if got := dependencyIDs(senderDependencyImages(history, question, nil, "bot")); got != "b#0,c#0" {
			t.Fatalf("dependencies = %s", got)
		}
	})
	t.Run("quote suppresses dependency images", func(t *testing.T) {
		quoted := question
		quoted.Quoted = &QuotedMessage{MessageID: "other", UserID: "winter", Segments: photoEvent("other", "winter", now-300).Segments}
		history := []MessageEvent{photoEvent("mine", "irony", now-14), quoted}
		if got := senderDependencyImages(history, quoted, nil, "bot"); len(got) != 0 {
			t.Fatalf("quoted image is the referent, got extra %s", dependencyIDs(got))
		}
	})
	t.Run("stops at bot reply to sender and at others' substance", func(t *testing.T) {
		answered := photoEvent("answered", "irony", now-40)
		reply := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "bot", MessageID: "bot-1", Time: now - 30, Outbound: true,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是只猫"}}}}
		history := []MessageEvent{answered, reply, question}
		if got := senderDependencyImages(history, question, nil, "bot"); len(got) != 0 {
			t.Fatalf("answered image is not pending: %s", dependencyIDs(got))
		}
		history = []MessageEvent{photoEvent("mine", "irony", now-40), textEvent("talk", "winter", "晚上吃啥", now-20), question}
		if got := senderDependencyImages(history, question, nil, "bot"); len(got) != 0 {
			t.Fatalf("another person's message should end the pending window: %s", dependencyIDs(got))
		}
		history = []MessageEvent{photoEvent("mine", "irony", now-40), stickerEvent("react", "winter", now-20), question}
		if got := dependencyIDs(senderDependencyImages(history, question, nil, "bot")); got != "mine#0" {
			t.Fatalf("someone else's sticker is not substance: %s", got)
		}
	})
	t.Run("same-turn supplement skipped", func(t *testing.T) {
		history := []MessageEvent{photoEvent("same-turn", "irony", now-5), question}
		if got := senderDependencyImages(history, question, map[string]bool{"same-turn": true}, "bot"); len(got) != 0 {
			t.Fatalf("same-turn supplement is attached elsewhere: %s", dependencyIDs(got))
		}
	})
}

func TestSenderDependencyDescriptionBudget(t *testing.T) {
	for images, want := range map[int]time.Duration{
		0: 0,
		1: 90 * time.Second,
		2: 120 * time.Second,
		4: 180 * time.Second,
		9: 4 * time.Minute,
	} {
		if got := senderDependencyDescriptionBudget(images); got != want {
			t.Fatalf("budget(%d) = %s, want %s", images, got, want)
		}
	}
}

// scriptedReplyProvider 按请求内容分流：识图请求给描述，审核给通过，回复请求交给 reply。
type scriptedReplyProvider struct {
	mu          sync.Mutex
	replies     []llm.GenerateRequest
	description string
	describeErr error
	reply       func(req llm.GenerateRequest) string
}

func requestText(req llm.GenerateRequest) string {
	var body strings.Builder
	for _, message := range req.Messages {
		body.WriteString(message.Content)
		for _, part := range message.Parts {
			body.WriteString(part.Text)
		}
		body.WriteString("\n")
	}
	return body.String()
}

func (p *scriptedReplyProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	switch {
	case strings.Contains(body, "请为这张图片生成可复用的客观中文描述"):
		if p.describeErr != nil {
			return nil, p.describeErr
		}
		return &llm.GenerateResponse{Model: "test", Text: p.description}, nil
	case strings.Contains(body, "send_confidence"):
		return &llm.GenerateResponse{Model: "test", Text: `{"send_confidence":0.99,"reason":"ok"}`}, nil
	}
	p.mu.Lock()
	p.replies = append(p.replies, req)
	p.mu.Unlock()
	text := "好的"
	if p.reply != nil {
		text = p.reply(req)
	}
	return &llm.GenerateResponse{Model: "test", Text: text}, nil
}

func (p *scriptedReplyProvider) dependencyMessages() []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []llm.Message
	for _, req := range p.replies {
		for _, message := range req.Messages {
			if strings.Contains(message.Content+firstPartText(message), "【同一发言者稍早发的图") {
				out = append(out, message)
			}
		}
	}
	return out
}

func firstPartText(message llm.Message) string {
	if len(message.Parts) == 0 {
		return ""
	}
	return message.Parts[0].Text
}

func dependencyTestRuntime(t *testing.T, provider LLMProvider, agent bool, plugins *PluginManager) (*Runtime, MessageEvent) {
	t.Helper()
	imagePath, hash := writeRecallImageFixture(t)
	if plugins == nil {
		plugins = NewPluginManager()
	}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", AgentEnabled: agent}, &recordingChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(newRecallImageTestStore())
	runtime.historyImageDescBackoff = -1
	photo := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "irony", SenderName: "irony", MessageID: "photo-1", Time: 1_800_000_000,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": imagePath, imageContentSHA256Key: hash}}},
	}
	runtime.remember(photo)
	question := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "irony", SenderName: "irony", MessageID: "q-1", Time: 1_800_000_014,
		RawMessage: "这是什么", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是什么"}}},
	}
	return runtime, question
}

// 对话模型能看图时，依赖图以原图单独附上、标明来历，当前消息本身不带图。
// agent 和非 agent 两条路都一样。
func TestSenderDependencyImagesAttachLabeledPixels(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent=%v", agent), func(t *testing.T) {
			provider := &scriptedReplyProvider{description: "一只橘猫"}
			runtime, question := dependencyTestRuntime(t, provider, agent, nil)
			if _, err := runtime.replyTo(context.Background(), question, question.RawMessage); err != nil {
				t.Fatal(err)
			}
			messages := provider.dependencyMessages()
			if len(messages) == 0 {
				t.Fatal("dependency image block never reached the reply model")
			}
			block := messages[len(messages)-1]
			if !llmMessageHasImagePart(block) {
				t.Fatalf("dependency block has no pixels: %#v", block)
			}
			var labels strings.Builder
			for _, part := range block.Parts {
				labels.WriteString(part.Text)
			}
			for _, want := range []string{"不是本条消息自带的", "图1：", "14 秒前发的图（原图", "message_id="} {
				if !strings.Contains(labels.String(), want) {
					t.Fatalf("label missing %q:\n%s", want, labels.String())
				}
			}
			provider.mu.Lock()
			last := provider.replies[len(provider.replies)-1]
			provider.mu.Unlock()
			current := last.Messages[len(last.Messages)-1]
			if llmMessageHasImagePart(current) {
				t.Fatal("dependency pixels leaked into the current message")
			}
		})
	}
}

// 识图插件设成「仅识别文字」：不附原图，等描述，把描述写进那一段。
func TestSenderDependencyImagesTextOnlyDeliveryWaitsForDescriptions(t *testing.T) {
	provider := &scriptedReplyProvider{description: "一只橘猫趴在键盘上"}
	manager := NewPluginManager(NewImageOCRPlugin(nil))
	if _, err := manager.UpdateSettings(imageOCRPluginID, map[string]any{"backend": imageOCRBackendDisabled, "delivery": imageOCRDeliveryText, "describe_enabled": true}); err != nil {
		t.Fatal(err)
	}
	runtime, question := dependencyTestRuntime(t, provider, false, manager)
	if runtime.chatModelReceivesImages(question) {
		t.Fatal("text-only delivery should mean the chat model cannot see images")
	}
	if _, err := runtime.replyTo(context.Background(), question, question.RawMessage); err != nil {
		t.Fatal(err)
	}
	messages := provider.dependencyMessages()
	if len(messages) == 0 {
		t.Fatal("dependency block missing")
	}
	block := messages[len(messages)-1]
	if llmMessageHasImagePart(block) {
		t.Fatal("text-only delivery must not attach pixels")
	}
	if !strings.Contains(block.Content, "画面描述：一只橘猫趴在键盘上") {
		t.Fatalf("description missing:\n%s", block.Content)
	}
}

// 描述没等到：照样回复，但写明哪张还没读到。
func TestSenderDependencyImagesTimeoutNotesUnreadImages(t *testing.T) {
	provider := &scriptedReplyProvider{describeErr: errors.New("vision down")}
	runtime, question := dependencyTestRuntime(t, provider, false, nil)
	images := senderDependencyImages(runtime.contextHistory(question), question, nil, "bot")
	if len(images) != 1 {
		t.Fatalf("images = %s", dependencyIDs(images))
	}
	message := runtime.senderDependencyMessage(context.Background(), question, &senderDependencyContext{images: images})
	if llmMessageHasImagePart(message) {
		t.Fatal("description mode attached pixels")
	}
	for _, want := range []string{"还没读到", "【还没读到的图】图1", "不要编内容"} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("missing %q:\n%s", want, message.Content)
		}
	}
}

// 附了原图、模型却说没收到图：不发这句，换成描述重来一次。
func TestSenderDependencyImagesRefusalFallsBackToDescriptions(t *testing.T) {
	provider := &scriptedReplyProvider{description: "一只橘猫"}
	provider.reply = func(req llm.GenerateRequest) string {
		for _, message := range req.Messages {
			if llmMessageHasImagePart(message) {
				return "没收到图片，你再发一遍吧"
			}
		}
		return "是一只橘猫"
	}
	runtime, question := dependencyTestRuntime(t, provider, false, nil)
	reply, err := runtime.replyTo(context.Background(), question, question.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "橘猫") {
		t.Fatalf("reply = %q, want the description-mode answer", reply)
	}
	messages := provider.dependencyMessages()
	if len(messages) < 2 || !llmMessageHasImagePart(messages[0]) || llmMessageHasImagePart(messages[len(messages)-1]) {
		t.Fatalf("expected a pixel attempt then a description retry, got %d blocks", len(messages))
	}
	if !strings.Contains(messages[len(messages)-1].Content, "画面描述：一只橘猫") {
		t.Fatalf("retry block missing description:\n%s", messages[len(messages)-1].Content)
	}
}

// 加急识图并行做，但不超过上限。
func TestUrgentHistoryImageDescriptionsRunInParallel(t *testing.T) {
	release := make(chan struct{})
	provider := &queueVisionProvider{behave: func(ctx context.Context, _ int) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	runtime, store := newQueueTestRuntime(t, provider, 5*time.Second, time.Hour)
	var events []MessageEvent
	var hashes []string
	for index := 0; index < 5; index++ {
		event, eventHashes := multiImageEvent(t, fmt.Sprintf("urgent-%d", index), 1)
		hash := fmt.Sprintf("%064x", 100+index)
		event.Segments[0].Data[imageContentSHA256Key] = hash
		eventHashes[0] = hash
		events = append(events, event)
		hashes = append(hashes, eventHashes...)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runtime.awaitHistoryImageDescriptions(ctx, events...)
	}()
	waitForCondition(t, 2*time.Second, func() bool {
		_, active := provider.snapshot()
		return active == historyImageDescriptionUrgentConcurrency
	})
	time.Sleep(100 * time.Millisecond)
	if _, active := provider.snapshot(); active > historyImageDescriptionUrgentConcurrency {
		t.Fatalf("urgent jobs exceeded the cap: %d", active)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("urgent descriptions did not finish")
	}
	if describedCount(store, hashes) != len(hashes) {
		t.Fatalf("described %d of %d", describedCount(store, hashes), len(hashes))
	}
}

// 前一条只发了一张图、没有文字：也算「连发未回」。
func TestPendingEarlierMessageAcceptsImageOnly(t *testing.T) {
	photo := photoEvent("photo", "irony", 1000)
	question := textEvent("q", "irony", "这是什么", 1010)
	earlier, ok := pendingEarlierMessage([]MessageEvent{photo, question}, question)
	if !ok || earlier.MessageID != "photo" {
		t.Fatalf("image-only earlier message not pending: %#v ok=%v", earlier, ok)
	}
	if prompt := replyDecorationPrompt(BotConfig{}, question, []MessageEvent{photo, question}); !strings.Contains(prompt, "他刚发了一张图") {
		t.Fatalf("decoration prompt = %q", prompt)
	}
	if _, ok := pendingEarlierMessage([]MessageEvent{stickerEvent("s", "irony", 1000), question}, question); ok {
		t.Fatal("a sticker is a reaction, not a pending message")
	}
}

// 事故复现：当前消息带着一张图，模型点名了 Winter 的头像，用的必须是头像。
func TestExplicitIdentitySourceBeatsCurrentMessageImage(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {"items": []any{map[string]any{"group_id": "123456", "user_id": "20002", "nickname": "Winter"}}},
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", SenderName: "irony", MessageID: "q",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "http://x/sticker.gif"}},
			{Type: "text", Data: map[string]string{"text": "能不能把 winter 姐姐头像变成机器人风格"}},
		},
	}
	urls, used, err := runtime.resolveImageEditSources(context.Background(), event, imageEditSourcePlan{IdentitySources: []string{avatarSourceMemberPrefix + "20002"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != OneBotMemberAvatarURL("20002") {
		t.Fatalf("sources = %#v", urls)
	}
	if len(used) != 1 || used[0].Kind != "member_avatar" || used[0].UserID != "20002" {
		t.Fatalf("sources_used = %#v", used)
	}
	// 点名的人取不到头像：报错，不退回当前消息里那张图。
	if _, _, err := runtime.resolveImageEditSources(context.Background(), event, imageEditSourcePlan{IdentitySources: []string{avatarSourceMemberPrefix + "99999"}}); err == nil {
		t.Fatal("unresolvable identity source silently fell back")
	}
	// 什么都没点名时，当前消息里的图仍是第一选择。
	urls, used, _ = runtime.resolveImageEditSources(context.Background(), event, imageEditSourcePlan{})
	if len(urls) != 1 || urls[0] != "http://x/sticker.gif" || used[0].Kind != imageSourceKindCurrentMessage {
		t.Fatalf("implicit sources = %#v used=%#v", urls, used)
	}
}

// 同一个人刚发的图只是候选：agent 调工具时不会被隐式当原图；意图路由那条路才兜底，
// 而且只认发言者自己的图。
func TestDependencyImageNotUsedAsImplicitEditSource(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	dataPhoto := func(messageID, userID, payload string, at int64) MessageEvent {
		event := photoEvent(messageID, userID, at)
		event.Segments[0].Data = map[string]string{"url": "data:image/png;base64," + payload}
		return event
	}
	runtime.remember(dataPhoto("other", "winter", "b3RoZXI=", 990))
	runtime.remember(dataPhoto("mine", "irony", "bWluZQ==", 1000))
	question := textEvent("q", "irony", "改成黑白", 1010)
	if urls, _, _ := runtime.resolveImageEditSources(context.Background(), question, imageEditSourcePlan{}); len(urls) != 0 {
		t.Fatalf("agent path used a dependency image implicitly: %#v", urls)
	}
	urls, used, err := runtime.resolveImageEditSources(context.Background(), question, imageEditSourcePlan{SourceMessageIDs: []string{"mine"}})
	if err != nil || len(urls) != 1 || used[0].Kind != imageSourceKindMessage || used[0].MessageID != "mine" {
		t.Fatalf("explicit message source: urls=%#v used=%#v err=%v", urls, used, err)
	}
	if got := runtime.imageEditSourceImages(context.Background(), question, nil); len(got) != 1 || got[0] != "data:image/png;base64,bWluZQ==" {
		t.Fatalf("intent path fallback = %#v, want only the sender's own image", got)
	}
	winterAsks := textEvent("q2", "winter", "改成黑白", 1011)
	history := []MessageEvent{photoEvent("mine", "irony", 1000), winterAsks}
	if got := recentHistoryImageBatch(history, winterAsks); len(got) != 0 {
		t.Fatalf("another member's image was picked: %#v", got)
	}
}

func TestImageToolOperationFollowsSources(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", Model: "gpt-test", ImageModel: "gpt-image-2",
	})}, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy}

	request, err := tool.prepareRequest(map[string]any{"prompt": "机器人风格", "identity_sources": []any{"member_avatar:20002"}})
	if err != nil || request.Operation != "edit" {
		t.Fatalf("missing operation with identity_sources: op=%q err=%v", request.Operation, err)
	}
	request, err = tool.prepareRequest(map[string]any{"prompt": "机器人风格", "source_message_ids": []any{"123"}})
	if err != nil || request.Operation != "edit" {
		t.Fatalf("missing operation with source_message_ids: op=%q err=%v", request.Operation, err)
	}
	if _, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "机器人风格", "identity_sources": []any{"member_avatar:20002"}}); err == nil || !strings.Contains(err.Error(), "generate") {
		t.Fatalf("generate with sources should be rejected, err=%v", err)
	}
	request, err = tool.prepareRequest(map[string]any{"prompt": "一只猫"})
	if err != nil || request.Operation != "generate" {
		t.Fatalf("no sources should stay generate: op=%q err=%v", request.Operation, err)
	}
	withImage := event
	withImage.Segments = []MessageSegment{{Type: "image", Data: map[string]string{"url": "http://x/a.jpg"}}}
	imageTool := &dianaImageTool{runtime: runtime, event: withImage, relationship: policy}
	if request, err := imageTool.prepareRequest(map[string]any{"prompt": "改成黑白"}); err != nil || request.Operation != "edit" {
		t.Fatalf("current image without operation: op=%q err=%v", request.Operation, err)
	}
}

// 工具结果里写明这次真正用的是谁的头像。
func TestImageToolResultReportsSourcesUsed(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {"items": []any{map[string]any{"group_id": "123456", "user_id": "20002", "nickname": "Winter"}}},
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: "http://127.0.0.1:1/v1", Model: "gpt-test", ImageModel: "gpt-image-2",
	})}, nil, nil, nil)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002", SenderName: "Winter", MessageID: "w1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "hi"}}}})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", SenderName: "irony", MessageID: "q",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "http://x/sticker.gif"}},
			{Type: "text", Data: map[string]string{"text": "把 winter 头像变成机器人风格"}},
		},
	}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy}
	request, err := tool.prepareRequest(map[string]any{"prompt": "机器人风格", "identity_sources": []any{"member_avatar:20002"}})
	if err != nil {
		t.Fatal(err)
	}
	// 带着开场白收集器：任务只预约不启动，测试不用真去调图片接口。
	ctx, _ := withImageAnnouncementSink(context.Background())
	result, err := tool.enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result)
	for _, want := range []string{`"sources_used"`, `"kind":"member_avatar"`, `"user_id":"20002"`, `"user":"Winter"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("result missing %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), imageSourceKindCurrentMessage) {
		t.Fatalf("current-message sticker was used: %s", body)
	}
}
