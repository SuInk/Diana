// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

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
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "s.gif", "sub_type": "1", "url": "http://x/s.gif"}}},
	}
}

func photoEvent(messageID, userID string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, MessageID: messageID, Time: at,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "p.jpg", "url": "http://x/p.jpg"}}},
	}
}

// 最常见的那条路必须一分钱都不多花：自己发图、紧接着问，附近没有别人的发言，
// 直接合并，不问模型。
func TestAdjacentMediaMergesWithoutLLMWhenNothingElseToMean(t *testing.T) {
	var llmCalls atomic.Int32
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: `{"refers_to_media":false,"confidence":0.9}`}, nil
	})
	photo := photoEvent("photo-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(photo)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{photo})
	if !outcome.Merge || outcome.Method != "no_competing_referent" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("没有歧义时不该问模型，llm calls = %d", got)
	}
}

// 文本明说在讲图，同样不问模型。
func TestExplicitMediaReferenceMergesWithoutLLM(t *testing.T) {
	var llmCalls atomic.Int32
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: `{"refers_to_media":false,"confidence":0.9}`}, nil
	})
	owner := textEvent("owner-1", "99999", "这是我们的 API Key：sk-xxxx", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这张图什么意思", 1006)
	runtime.remember(owner)
	runtime.remember(sticker)

	for _, text := range []string{"这张图什么意思", "图里写的啥", "这个表情什么梗", "刚才那张截图呢"} {
		outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, text, []MessageEvent{sticker})
		if !outcome.Merge || outcome.Method != "explicit_media_reference" {
			t.Fatalf("%q → %#v", text, outcome)
		}
	}
	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("显式指向媒体时不该问模型，llm calls = %d", got)
	}
}

// 截图里那一局：群主发 API Key，群友甩了个表情，再问「这是什么」。
// 有竞争指代对象，必须交给模型判，模型说不是图就不合并。
func TestStickerReactionDoesNotSwallowTheQuestion(t *testing.T) {
	var prompt atomic.Value
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &promptCapturingProvider{
			reply:  `{"refers_to_media":false,"confidence":0.85,"reason":"表情只是对上一条的反应"}`,
			prompt: &prompt,
		}, nil
	})
	owner := textEvent("owner-1", "99999", "偷啃滞销，帮帮我们喵，使用以下 API Key 可以免费使用 GPT 模型", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(owner)
	runtime.remember(sticker)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{sticker})
	if outcome.Merge {
		t.Fatalf("表情不该被当成「这」的指代对象：%#v", outcome)
	}
	if outcome.Method != "llm_declined" {
		t.Fatalf("method = %q", outcome.Method)
	}
	// 判断器得同时看到那条表情和群主那句话，否则它没得判。
	sent, _ := prompt.Load().(string)
	if !strings.Contains(sent, "API Key") {
		t.Fatalf("竞争指代对象没有喂给判断器：%s", sent)
	}
	if !strings.Contains(sent, `"is_sticker":true`) {
		t.Fatalf("贴纸线索没有喂给判断器：%s", sent)
	}
}

// 模型说「就是在问这张图」时照常合并。
func TestLLMConfirmedMergeStillHappens(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: `{"refers_to_media":true,"confidence":0.9,"reason":"刚发的照片"}`}, nil
	})
	other := textEvent("other-1", "99999", "今天天气不错", 990)
	photo := photoEvent("photo-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(other)
	runtime.remember(photo)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{photo})
	if !outcome.Merge || outcome.Method != "llm_confirmed" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

// 置信度不够就不合并——错并是不可逆的，宁可少贴一张图。
func TestLowConfidenceDoesNotMerge(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: `{"refers_to_media":true,"confidence":0.3}`}, nil
	})
	other := textEvent("other-1", "99999", "这串是什么东西", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(other)
	runtime.remember(sticker)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{sticker})
	if outcome.Merge {
		t.Fatalf("置信度 0.3 不该合并：%#v", outcome)
	}
}

// 模型不可用时同样不合并，并且如实记下原因。
func TestLLMFailureDoesNotMerge(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return nil, errProviderUnavailableForTest
	})
	other := textEvent("other-1", "99999", "这串是什么东西", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(other)
	runtime.remember(sticker)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{sticker})
	if outcome.Merge || outcome.Method != "llm_unavailable" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if strings.TrimSpace(outcome.Reason) == "" {
		t.Fatal("失败原因要记下来，否则查不出为什么没合并")
	}
}

// 自己刚说过的话不算竞争对象：并不并图都不影响模型读到那句话。
func TestOwnEarlierMessageIsNotCompeting(t *testing.T) {
	var llmCalls atomic.Int32
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: `{"refers_to_media":false,"confidence":0.9}`}, nil
	})
	mine := textEvent("mine-1", "10001", "我拍了张照片", 990)
	photo := photoEvent("photo-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(mine)
	runtime.remember(photo)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{photo})
	if !outcome.Merge || outcome.Method != "no_competing_referent" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("llm calls = %d", got)
	}
}

// promptCapturingProvider 把送出去的提示词留下来，用来确认判断器真的看到了
// 它需要的线索——不然「模型说不合并」可能只是它瞎蒙对了。
type promptCapturingProvider struct {
	reply  string
	prompt *atomic.Value
}

func (p *promptCapturingProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	var builder strings.Builder
	for _, message := range req.Messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	p.prompt.Store(builder.String())
	return &llm.GenerateResponse{Text: p.reply}, nil
}

var errProviderUnavailableForTest = errors.New("provider unavailable")

func TestEventLooksLikeSticker(t *testing.T) {
	if !eventLooksLikeSticker(stickerEvent("s", "1", 0)) {
		t.Fatal("sub_type=1 的 image 应当算表情")
	}
	if eventLooksLikeSticker(photoEvent("p", "1", 0)) {
		t.Fatal("普通照片不该算表情")
	}
	mface := MessageEvent{Segments: []MessageSegment{{Type: "mface", Data: map[string]string{"emoji_id": "x"}}}}
	if !eventLooksLikeSticker(mface) {
		t.Fatal("mface 应当算表情")
	}
	// sub_type=0 是普通图片，别误伤。
	zero := MessageEvent{Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "a.jpg", "sub_type": "0"}}}}
	if eventLooksLikeSticker(zero) {
		t.Fatal("sub_type=0 是普通图片")
	}
}
