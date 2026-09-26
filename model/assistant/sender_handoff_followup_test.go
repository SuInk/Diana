// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// silentSideEffectProvider：图那一轮调了一个写外部系统的工具，随后出错、什么都没发。
type silentSideEffectProvider struct {
	photoEntered  chan struct{}
	photoGate     chan struct{}
	questionReady chan struct{}
	questionGate  chan error
	photoOnce     sync.Once
	questionOnce  sync.Once
	photoRuns     atomic.Int32
}

func (p *silentSideEffectProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	switch {
	case strings.Contains(body, "请为这张图片生成可复用的客观中文描述"):
		return &llm.GenerateResponse{Model: "test", Text: "一只橘猫"}, nil
	case strings.Contains(body, "send_confidence"):
		return &llm.GenerateResponse{Model: "test", Text: `{"send_confidence":0.99,"reason":"ok"}`}, nil
	case strings.Contains(body, "【同一发言者稍早发的图"):
		p.questionOnce.Do(func() { close(p.questionReady) })
		if err := <-p.questionGate; err != nil {
			return nil, err
		}
		return &llm.GenerateResponse{Model: "test", Text: "这是一只橘猫"}, nil
	case !strings.Contains(body, "橘猫叫什么名字") && !strings.Contains(body, "model down") && !strings.Contains(body, "tool exploded"):
		p.photoRuns.Add(1)
		p.photoOnce.Do(func() { close(p.photoEntered) })
		<-p.photoGate
		markExternalSideEffect(ctx)
		return nil, errors.New("tool exploded")
	}
	return &llm.GenerateResponse{Model: "test", Text: "好的"}, nil
}

// 写过外部系统之后出错、一条都没发的图那一轮：副作用在工具调用那一刻就同步给交接登记，
// 它不记成交出去；文字那一轮随后没回出去，也不会把它放回队列重跑（工具会再调一遍）。
func TestSideEffectWithoutSendIsNeverHandedOff(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	provider := &silentSideEffectProvider{photoEntered: make(chan struct{}), photoGate: make(chan struct{}), questionReady: make(chan struct{}), questionGate: make(chan error, 1)}
	errorNotify := false
	runtime := NewRuntime(BotConfig{BotAccount: "bot", ErrorNotifyEnabled: &errorNotify}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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
		RawMessage: "橘猫叫什么名字", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "橘猫叫什么名字"}}},
	}
	runtime.noteSenderTurnArrival(photo)
	runtime.noteSenderTurnArrival(question)

	photoDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), photo, "", "replied")
		photoDone <- outcome
	}()
	select {
	case <-provider.photoEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("image turn never started generating")
	}
	runtime.noteSenderTurnMergeChecked(question, "photo-1", true)
	questionDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), question, question.RawMessage, "replied")
		questionDone <- outcome
	}()
	select {
	case <-provider.questionReady:
	case <-time.After(5 * time.Second):
		t.Fatal("question turn never started generating")
	}
	if _, taken := runtime.senderTurnSupersededBy(photo); !taken {
		t.Fatal("setup: the text turn should have taken the image over")
	}
	close(provider.photoGate)
	select {
	case outcome := <-photoDone:
		if outcome == inboundOutcomeHandedOffPending || outcome == "superseded_follow_up" {
			t.Fatalf("a turn that wrote to an external system was recorded as handed off: %q", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("image turn never finished")
	}
	if len(runtime.channel.(*recordingChannel).sentSnapshot()) != 0 {
		t.Fatal("setup: the image turn should have sent nothing")
	}
	provider.questionGate <- errors.New("model down")
	<-questionDone
	time.Sleep(300 * time.Millisecond)
	if runs := provider.photoRuns.Load(); runs != 1 {
		t.Fatalf("image turn ran %d times; it must not be re-run after its side effect", runs)
	}
}

// prefixCommandPlugin 只认整句「#cmd」，和 #diana 状态卡片一样是严格格式的指令。
type prefixCommandPlugin struct{}

func (prefixCommandPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "prefix-command-test", Name: "prefix command", BuiltIn: true}
}

func (prefixCommandPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (prefixCommandPlugin) ShouldHandle(_ MessageEvent, text string) bool {
	return strings.TrimSpace(text) == "#cmd"
}

// 带 @机器人 或称呼前缀的插件指令也是指令：后一条不能把它接走。
func TestSenderBurstNeverAbsorbsPrefixedPluginCommands(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event MessageEvent
	}{
		{name: "at bot", event: directedGroupMessage("20311", "10001", "#cmd")},
		{name: "trigger word", event: textEvent("20311", "10001", "diana #cmd", 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &burstReplyProvider{}
			disabled := false
			runtime := NewRuntime(BotConfig{BotAccount: "42", GroupTriggers: []string{"diana"}, BotReplyLoopDetectionEnabled: &disabled},
				&recordingChannel{}, NewPluginManager(prefixCommandPlugin{}), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			command := tc.event
			command.Time = 1_800_000_000
			later := directedGroupMessage("20312", "10001", "顺便问下今天吃什么")
			later.Time = 1_800_000_002
			arriveTogether(runtime, command, later)
			if runtime.burstChatMessage(command, directedInboundText(command)) {
				t.Fatalf("%q should be recognised as a plugin command", directedInboundText(command))
			}
			if outcome, err := runtime.replyAndRecord(context.Background(), later, "顺便问下今天吃什么", "replied"); err != nil || outcome != "replied" {
				t.Fatalf("later outcome=%q err=%v", outcome, err)
			}
			if _, taken := runtime.senderTurnSupersededBy(command); taken {
				t.Fatal("a prefixed plugin command was taken over by a later message")
			}
		})
	}
}

// 主人命令只有一张表：执行和纯判断都从它走。表里每一条都得被纯判断认出来，
// 执行那边也确实接得住（这里只跑没有副作用的几条）。
func TestOwnerCommandTableDrivesBothPaths(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, owner := range ownerCommands {
		text := owner.exact
		if text == "" {
			text = owner.prefix + "abc"
		}
		event := privateEvent("10001", "20321", text)
		if !runtime.wouldHandleOwnerCommand(event, text) {
			t.Fatalf("owner command %q is not recognised by the pure check", text)
		}
		if runtime.wouldHandleOwnerCommand(privateEvent("10002", "20322", text), text) {
			t.Fatalf("%q from a non-owner must not count as an owner command", text)
		}
	}
	for _, text := range []string{"帮助", "菜单", "群 列表"} {
		if _, handled := runtime.handleOwnerCommand(privateEvent("10001", "20323", text), text); !handled {
			t.Fatalf("handleOwnerCommand did not handle %q", text)
		}
	}
	if runtime.wouldHandleOwnerCommand(privateEvent("10001", "20324", "今天天气怎么样"), "今天天气怎么样") {
		t.Fatal("ordinary chat is not an owner command")
	}
}
