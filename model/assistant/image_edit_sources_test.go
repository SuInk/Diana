// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	editSourceBotID    = "10000"
	editSourceUserID   = "10001"
	editSourceGroupID  = "20001"
	editSourceOriginal = "data:image/png;base64,b3JpZ2luYWw="
	editSourceNewer    = "data:image/png;base64,bmV3ZXI="
)

func newEditSourceRuntime() *Runtime {
	return NewRuntime(BotConfig{BotAccount: editSourceBotID, RecentContextLimit: 40}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
}

func editSourceText(id, userID, text string, extra ...MessageSegment) MessageEvent {
	segments := append(append([]MessageSegment(nil), extra...), MessageSegment{Type: "text", Data: map[string]string{"text": text}})
	return MessageEvent{
		Kind: EventKindGroup, GroupID: editSourceGroupID, SelfID: editSourceBotID,
		UserID: userID, MessageID: id, RawMessage: text, Segments: segments,
	}
}

func editSourceImage(id, imageURL string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: editSourceGroupID, SelfID: editSourceBotID,
		UserID: editSourceUserID, MessageID: id,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURL}}},
	}
}

func replySegment(id string) MessageSegment {
	return MessageSegment{Type: "reply", Data: map[string]string{"id": id}}
}

// 群里刷过好几条之后，最近聊天记录已经够不着原图，只能靠引用链。
func padEditSourceChatter(runtime *Runtime, count int) {
	for index := 0; index < count; index++ {
		runtime.remember(editSourceText("chatter-"+strconv.Itoa(index), "30000", "路过聊两句"))
	}
}

func quotedFrom(event MessageEvent) *QuotedMessage {
	return &QuotedMessage{
		MessageID: event.MessageID, UserID: event.UserID, GroupID: event.GroupID,
		RawMessage: event.RawMessage, Segments: event.Segments,
	}
}

// 用户引用原图说「改一下」，机器人回「在画了」时又引用了那条；用户再引用机器人
// 的回复说「继续」，原图在两层引用之外。
func TestImageEditSourcesFollowQuoteChain(t *testing.T) {
	runtime := newEditSourceRuntime()
	runtime.remember(editSourceImage("original", editSourceOriginal))
	request := editSourceText("request", editSourceUserID, "改成赛璐璐风", replySegment("original"))
	runtime.remember(request)
	botReply := editSourceText("bot-reply", editSourceBotID, "在画了喵", replySegment("request"))
	runtime.remember(botReply)
	padEditSourceChatter(runtime, 6)

	event := editSourceText("continue", editSourceUserID, "继续", replySegment("bot-reply"))
	event.Quoted = quotedFrom(botReply)
	got := runtime.imageEditSourceImages(context.Background(), event, nil)
	if strings.Join(got, ",") != editSourceOriginal {
		t.Fatalf("sources = %#v, want original via quote chain", got)
	}
}

// 引用机器人那条失败通知（纯文字、不再引用任何消息）说「重试」：原图只能从
// 上一次改图任务记下的来源里找回来。
func TestImageEditSourcesRetryFromFailureNoticeUsesLastTaskSources(t *testing.T) {
	runtime := newEditSourceRuntime()
	runtime.remember(editSourceImage("original", editSourceOriginal))
	previous := editSourceText("request", editSourceUserID, "改一下", replySegment("original"))
	runtime.imageEditSources.remember(sessionKey(previous), []string{editSourceOriginal}, time.Now())
	notice := editSourceText("notice", editSourceBotID, "后台任务「图片编辑」执行失败：unexpected EOF")
	runtime.remember(notice)
	// 期间群里又有人发了别的图：引用失败通知说重试，指的仍然是上一次那张。
	runtime.remember(editSourceImage("unrelated", editSourceNewer))

	event := editSourceText("retry", editSourceUserID, "重试", replySegment("notice"))
	event.Quoted = quotedFrom(notice)
	got := runtime.imageEditSourceImages(context.Background(), event, nil)
	if strings.Join(got, ",") != editSourceOriginal {
		t.Fatalf("sources = %#v, want last task source", got)
	}
}

// 没有引用机器人的消息时，最近聊天里的新图优先于上一次任务的原图；
// 上一次任务的原图只是最后兜底，并且过期作废。
func TestImageEditSourcesLastTaskIsFallbackOnly(t *testing.T) {
	runtime := newEditSourceRuntime()
	event := editSourceText("ask", editSourceUserID, "把这张改成黑白")
	runtime.imageEditSources.remember(sessionKey(event), []string{editSourceOriginal}, time.Now())
	runtime.remember(editSourceImage("fresh", editSourceNewer))
	if got := runtime.imageEditSourceImages(context.Background(), event, nil); strings.Join(got, ",") != editSourceNewer {
		t.Fatalf("sources = %#v, want fresh history image", got)
	}

	empty := newEditSourceRuntime()
	empty.imageEditSources.remember(sessionKey(event), []string{editSourceOriginal}, time.Now())
	if got := empty.imageEditSourceImages(context.Background(), event, nil); strings.Join(got, ",") != editSourceOriginal {
		t.Fatalf("sources = %#v, want last task fallback", got)
	}

	expired := newEditSourceRuntime()
	expired.imageEditSources.remember(sessionKey(event), []string{editSourceOriginal}, time.Now().Add(-imageEditSourceMemoryTTL-time.Minute))
	if got := expired.imageEditSourceImages(context.Background(), event, nil); len(got) != 0 {
		t.Fatalf("sources = %#v, want none after TTL", got)
	}
}

func TestImageEditSourceMemoryIsBounded(t *testing.T) {
	var memory imageEditSourceMemory
	base := time.Now()
	for index := 0; index < imageEditSourceMemoryLimit+5; index++ {
		memory.remember("session-"+strconv.Itoa(index), []string{"a"}, base.Add(time.Duration(index)*time.Second))
	}
	if len(memory.entries) != imageEditSourceMemoryLimit {
		t.Fatalf("entries = %d", len(memory.entries))
	}
	if got := memory.recall("session-0", base.Add(time.Hour/4)); got != nil {
		t.Fatalf("oldest session should be evicted, got %#v", got)
	}
}

// 找不到原图时工具当场报错，不受理任务：模型不会先说「在画了」，
// 群里也不会再冒出一条后台任务失败通知。成功受理时记下原图，给之后的重试用。
func TestImageToolEditResolvesSourcesBeforeQueueing(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: "https://example.invalid/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2",
	})}
	runtime := NewRuntime(BotConfig{BotAccount: editSourceBotID}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	policy := RelationshipPolicy{AllowImageEditing: true}

	missing := editSourceText("no-image", editSourceUserID, "磨皮算一笔")
	_, err := newDianaImageTool(runtime, missing, policy).Run(context.Background(), map[string]any{"operation": "edit", "prompt": "磨皮"})
	if !errors.Is(err, errImageEditSourceNotFound) {
		t.Fatalf("Run() error = %v, want errImageEditSourceNotFound", err)
	}
	if count := runtime.activeSubagentTaskCount(); count != 0 {
		t.Fatalf("queued %d tasks without a source image", count)
	}

	withImage := editSourceImage("with-image", editSourceOriginal)
	tool := newDianaImageTool(runtime, withImage, policy).(*dianaImageTool)
	request, err := tool.prepareRequest(map[string]any{"operation": "edit", "prompt": "改成黑白"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = tool.enqueue(ctx, request)
	if got := runtime.imageEditSources.recall(sessionKey(withImage), time.Now()); strings.Join(got, ",") != editSourceOriginal {
		t.Fatalf("remembered sources = %#v", got)
	}
}

func TestPublicChatErrorMessageForImageEditFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errImageEditSourceNotFound, "没有找到要编辑的图片"},
		{errors.New("llm: openai-compatible request failed: 400 Bad Request: code=invalid_image_file; type=image_generation_user_error; message=Invalid image data."), "图片接口拒绝了这张原图"},
		{errors.New("unexpected EOF"), "连接中途断开"},
	} {
		got := publicChatErrorMessage(tc.err)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("publicChatErrorMessage(%v) = %q, want contains %q", tc.err, got, tc.want)
		}
		if strings.Contains(got, "不要对用户说") || strings.Contains(strings.ToLower(got), "eof") {
			t.Fatalf("public message leaks internal wording: %q", got)
		}
	}
}

// 模型自己做指代：认出原图在哪几条消息里，按 message_id 交给工具。多条消息、
// 一条消息多张图都要按指认顺序全部带上，而不是只取第一张。
func TestImageToolEditUsesModelSelectedSourceMessages(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: "https://example.invalid/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2",
	})}
	runtime := NewRuntime(BotConfig{BotAccount: editSourceBotID, RecentContextLimit: 40}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	const (
		secondA = "data:image/png;base64,c2Vjb25kLWE="
		secondB = "data:image/png;base64,c2Vjb25kLWI="
	)
	runtime.remember(editSourceImage("first", editSourceOriginal))
	pair := editSourceImage("pair", secondA)
	pair.Segments = append(pair.Segments, MessageSegment{Type: "image", Data: map[string]string{"url": secondB}})
	runtime.remember(pair)
	// 更近的一张无关图：自动猜会拿它，模型的指认要优先。
	runtime.remember(editSourceImage("unrelated", editSourceNewer))

	event := editSourceText("ask", editSourceUserID, "把前面那三张都改成黑白")
	tool := newDianaImageTool(runtime, event, RelationshipPolicy{AllowImageEditing: true}).(*dianaImageTool)
	request, err := tool.prepareRequest(map[string]any{
		"operation": "edit", "prompt": "黑白", "source_mode": "each",
		"source_message_ids": []any{"pair", "first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = tool.enqueue(ctx, request)
	want := []string{secondA, secondB, editSourceOriginal}
	if got := runtime.imageEditSources.recall(sessionKey(event), time.Now()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %#v, want %#v", got, want)
	}

	// 指错消息（不存在、或那条没图）要当场报给模型，不能悄悄换成别的图。
	queuedBefore := runtime.activeSubagentTaskCount()
	for _, ids := range [][]any{{"no-such-message"}, {"ask"}} {
		runtime.remember(event)
		bad, err := tool.prepareRequest(map[string]any{"operation": "edit", "prompt": "黑白", "source_message_ids": ids})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.enqueue(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "source_message_ids 有误") {
			t.Fatalf("enqueue(%v) error = %v", ids, err)
		}
	}
	if count := runtime.activeSubagentTaskCount(); count != queuedBefore {
		t.Fatalf("queued %d extra tasks with wrong source messages", count-queuedBefore)
	}
}
