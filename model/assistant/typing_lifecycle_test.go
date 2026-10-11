// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type typingLifecycleProvider func(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error)

func (p typingLifecycleProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return p(ctx, req)
}

type typingLifecycleChannel struct {
	typingOrderChannel
	action    func(context.Context) error
	transport func(context.Context) error
}

func (c *typingLifecycleChannel) SendChatAction(ctx context.Context, _ OutgoingMessage, _ string) error {
	c.record("typing")
	if c.action != nil {
		return c.action(ctx)
	}
	return nil
}

func (c *typingLifecycleChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	c.record("send:" + msg.Text)
	if c.transport != nil {
		return c.transport(ctx)
	}
	return nil
}

func (c *typingLifecycleChannel) CallAPI(ctx context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.record(action)
	if c.transport != nil {
		if err := c.transport(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]any{"message_id": "sent"}, nil
}

func (*typingLifecycleChannel) OutboundBackoffEnabled() bool { return true }
func (*typingLifecycleChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "10000"}
}

func newLifecycleTypingIndicator(t *testing.T, channel ChatActionChannel) *typingIndicator {
	t.Helper()
	indicator := &typingIndicator{
		channel: channel, interval: 5 * time.Millisecond, callTimeout: time.Hour,
		kick: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go indicator.run(t.Context())
	t.Cleanup(indicator.stop)
	return indicator
}

func typingLifecycleCount(channel *typingLifecycleChannel) int {
	count := 0
	for _, event := range channel.snapshot() {
		if event == "typing" {
			count++
		}
	}
	return count
}

func TestTypingLifecyclePublicGenerationResumes(t *testing.T) {
	channel := &typingLifecycleChannel{}
	indicator := newLifecycleTypingIndicator(t, channel)
	waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > 0 })
	indicator.pause()
	before := typingLifecycleCount(channel)
	provider := typingLifecycleProvider(func(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
		waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > before })
		return &llm.GenerateResponse{Text: "后续回复"}, nil
	})
	cfg := BotConfig{}.WithDefaults()
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001"}
	if _, err := runtime.generateReply(withTypingIndicator(t.Context(), indicator), cfg, event, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "继续"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTypingLifecycleCrossBatchQueueResumes(t *testing.T) {
	for _, decorated := range []bool{true, false} {
		name := "first_chunk"
		if decorated {
			name = "reply_batch"
		}
		t.Run(name, func(t *testing.T) {
			channel := &typingLifecycleChannel{}
			cfg := BotConfig{}.WithDefaults()
			runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, Platform: PlatformTelegram, GroupID: "10002", UserID: "10001"}
			indicator := newLifecycleTypingIndicator(t, channel)
			ctx, cancel := context.WithCancel(withTypingIndicator(t.Context(), indicator))
			defer cancel()
			waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > 0 })
			if _, err := runtime.deliverChunks(ctx, event, []string{"第一批"}, cfg, outboundDecoration{}); err != nil {
				t.Fatal(err)
			}
			before := typingLifecycleCount(channel)
			var unlock func()
			if decorated {
				unlock = runtime.lockReplyBatch(event)
			} else {
				gate := runtime.groupOutboundDelivery(event)
				gate.mu.Lock()
				unlock = gate.mu.Unlock
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if decorated {
					_, err = runtime.sendDecorated(ctx, event, "第二批", outboundDecoration{})
				} else {
					_, err = runtime.deliverChunks(ctx, event, []string{"", "第二批"}, cfg, outboundDecoration{})
				}
				done <- err
			}()
			defer func() {
				unlock()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(time.Second):
					t.Error("queued delivery did not finish")
				}
			}()
			waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > before })
		})
	}
}

func TestTypingLifecycleTransportCancelsBeforeSend(t *testing.T) {
	for _, forward := range []bool{false, true} {
		name := "ordinary"
		if forward {
			name = "forward"
		}
		t.Run(name, func(t *testing.T) {
			inflight := make(chan context.Context, 8)
			channel := &typingLifecycleChannel{action: func(ctx context.Context) error {
				inflight <- ctx
				<-ctx.Done()
				return ctx.Err()
			}}
			indicator := newLifecycleTypingIndicator(t, channel)
			var actionCtx context.Context
			select {
			case actionCtx = <-inflight:
			case <-time.After(time.Second):
				t.Fatal("typing did not start")
			}
			channel.transport = func(context.Context) error {
				if !errors.Is(actionCtx.Err(), context.Canceled) {
					t.Errorf("transport started before canceling in-flight typing: %v", actionCtx.Err())
				}
				return nil
			}
			runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001"}
			ctx := withTypingIndicator(t.Context(), indicator)
			if forward {
				if _, err := runtime.sendForwardReplyWithResult(ctx, event, "转发内容", runtime.effectiveConfigForEvent(event)); err != nil {
					t.Fatal(err)
				}
			} else if _, err := runtime.sendOutgoingWithResult(ctx, event, OutgoingMessage{Text: "普通内容"}); err != nil {
				t.Fatal(err)
			}
			before := typingLifecycleCount(channel)
			time.Sleep(30 * time.Millisecond)
			if got := typingLifecycleCount(channel); got != before {
				t.Fatalf("typing restarted after final delivery: %d -> %d", before, got)
			}
		})
	}
}

func TestTypingLifecycleRetryWaitResumes(t *testing.T) {
	withFastSendTiming(t)
	sendRetryBackoff = 40 * time.Millisecond
	for _, forward := range []bool{false, true} {
		name := "ordinary"
		if forward {
			name = "forward_payload_retry"
		}
		t.Run(name, func(t *testing.T) {
			inflight := make(chan context.Context, 8)
			channel := &typingLifecycleChannel{action: func(ctx context.Context) error {
				inflight <- ctx
				<-ctx.Done()
				return ctx.Err()
			}}
			indicator := newLifecycleTypingIndicator(t, channel)
			select {
			case <-inflight:
			case <-time.After(time.Second):
				t.Fatal("typing did not start")
			}
			attempts := 0
			channel.transport = func(context.Context) error {
				attempts++
				if attempts == 1 {
					return errors.New("unsupported forward node content")
				}
				select {
				case actionCtx := <-inflight:
					if !errors.Is(actionCtx.Err(), context.Canceled) {
						t.Errorf("retry transport did not cancel refreshed typing: %v", actionCtx.Err())
					}
				default:
					t.Error("typing was not resumed during retry wait")
				}
				return nil
			}
			runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001"}
			ctx := withTypingIndicator(t.Context(), indicator)
			var err error
			if forward {
				_, err = runtime.sendForwardReplyWithResult(ctx, event, "转发内容", runtime.effectiveConfigForEvent(event))
			} else {
				_, err = runtime.sendOutgoingWithResult(ctx, event, OutgoingMessage{Text: "普通内容"})
			}
			if err != nil || attempts != 2 {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestTypingLifecyclePluginFollowUpOwnsIndicator(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		disabled  bool
		reply     string
	}{
		{name: "live_parent", reply: "自然跟评"},
		{name: "cancelled_parent", cancelled: true, reply: "自然跟评"},
		{name: "disabled_masks_parent", disabled: true, reply: "自然跟评"},
		{name: "skip_stops_child", reply: "SKIP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &typingLifecycleChannel{}
			parent := newLifecycleTypingIndicator(t, channel)
			waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > 0 })
			parentCtx, cancel := context.WithCancel(withTypingIndicator(t.Context(), parent))
			defer cancel()
			if tc.cancelled {
				cancel()
				parent.stop()
			}
			var owned *typingIndicator
			calls := 0
			provider := typingLifecycleProvider(func(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
				calls++
				if err := ctx.Err(); err != nil {
					t.Fatalf("follow-up inherited parent cancellation: %v", err)
				}
				owned = typingIndicatorFromContext(ctx)
				if tc.disabled {
					if owned != nil {
						t.Fatal("disabled follow-up retained parent typing indicator")
					}
				} else {
					if owned == nil || owned == parent {
						t.Fatal("follow-up did not create its own typing indicator")
					}
					before := typingLifecycleCount(channel)
					owned.pause()
					owned.resume()
					waitForCondition(t, time.Second, func() bool { return typingLifecycleCount(channel) > before })
				}
				return &llm.GenerateResponse{Text: tc.reply}, nil
			})
			enabled := !tc.disabled
			cfg := BotConfig{QQTypingEnabled: &enabled}
			runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001"}
			runtime.maybeSendPluginFollowUp(parentCtx, event, PluginResponse{FollowUp: true, Reply: "链接解析结果"})
			if calls != 1 {
				t.Fatalf("model calls = %d", calls)
			}
			if owned != nil {
				select {
				case <-owned.done:
				default:
					t.Error("follow-up indicator was not stopped")
				}
			}
			before := typingLifecycleCount(channel)
			time.Sleep(30 * time.Millisecond)
			if got := typingLifecycleCount(channel); got != before {
				t.Fatalf("parent or child kept refreshing after follow-up: %d -> %d", before, got)
			}
			sends := 0
			for _, event := range channel.snapshot() {
				if strings.HasPrefix(event, "send:") {
					sends++
				}
			}
			want := 1
			if tc.reply == "SKIP" {
				want = 0
			}
			if sends != want {
				t.Fatalf("sent %d follow-ups, want %d", sends, want)
			}
		})
	}
}

func TestTypingLifecycleFinalSendFailureStaysPaused(t *testing.T) {
	withFastSendTiming(t)
	channel := &typingLifecycleChannel{}
	indicator := newLifecycleTypingIndicator(t, channel)
	channel.transport = func(context.Context) error { return errors.New("unsupported forward node content") }
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001"}
	ctx := withTypingIndicator(t.Context(), indicator)
	if _, err := runtime.sendOutgoingWithResult(ctx, event, OutgoingMessage{Text: "x"}); err == nil {
		t.Fatal("expected send failure")
	}
	before := typingLifecycleCount(channel)
	time.Sleep(60 * time.Millisecond)
	if got := typingLifecycleCount(channel); got != before {
		t.Fatalf("typing relit after final send failure: %d -> %d", before, got)
	}
}
