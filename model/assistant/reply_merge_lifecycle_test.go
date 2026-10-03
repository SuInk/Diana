// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 对话、解析与指令共用同一套准入规则，根消息和后来消息都检查。
func TestReplyMergeAdmissionByCapability(t *testing.T) {
	for _, kind := range []EventKind{EventKindGroup, EventKindPrivate} {
		for _, text := range []string{"解释 stdout", "#cmd", "Diana #cmd", "#diana", "清空上下文", "https://x.com/example/status/123456789"} {
			t.Run(string(kind)+"/"+text, func(t *testing.T) {
				p := &topicTestProvider{result: `{"relation":"repeat","confidence":0.99}`}
				plugins := NewPluginManager(prefixCommandPlugin{}, duplicateResolverPlugin{}, NewStatusCommandPlugin())
				r := NewRuntime(BotConfig{ID: "merge-test", BotAccount: "42", OwnerID: "user", GroupTriggers: []string{"Diana"}}, &recordingChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) { return p, nil })
				if _, err := plugins.SetEnabledForProfile(statusCommandPluginID, r.profileOrder[0], true); err != nil {
					t.Fatal(err)
				}
				makeEvent := func(id, body string) MessageEvent {
					e := privateEvent("user", id, body)
					e.Kind = kind
					if kind == EventKindGroup {
						e.GroupID = "123456"
					}
					return e
				}
				chat := text == "解释 stdout"
				root := makeEvent("root", text)
				ctx, finish := r.beginDirectReply(context.Background(), root)
				_, active := ctx.Value(directReplyRunContextKey{}).(directReplyRunContext)
				if active != chat {
					t.Fatalf("root admission=%v, want %v", active, chat)
				}
				finish()
				ctx, finish = r.beginDirectReply(context.Background(), makeEvent("chat-root", "解释 stdout"))
				defer finish()
				follow := makeEvent("follow", text)
				if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, text); merged != chat {
					t.Fatalf("incoming admission=%v, want %v", merged, chat)
				}
			})
		}
	}
}

type lifecycleDialogueProvider struct {
	mu         sync.Mutex
	requests   []llm.GenerateRequest
	started    chan int
	release    chan struct{}
	failure    error
	sideEffect bool
	reply      string
}

func (p *lifecycleDialogueProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	if strings.Contains(body, directReplyTopicPrompt) {
		return &llm.GenerateResponse{Text: `{"relation":"supplement","confidence":0.99}`}, nil
	}
	if strings.Contains(body, "发送前审核器") {
		return &llm.GenerateResponse{Text: `{"send_confidence":0.99,"account_safe":true}`}, nil
	}
	if strings.Contains(body, `"action":"none"`) {
		return &llm.GenerateResponse{Text: `{"action":"none","prompt":""}`}, nil
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	call := len(p.requests)
	p.mu.Unlock()
	p.started <- call
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.failure != nil {
		return nil, p.failure
	}
	if p.sideEffect {
		markExternalSideEffect(ctx)
	}
	return &llm.GenerateResponse{Text: firstNonEmpty(p.reply, "回答全部已接受的追问")}, nil
}

func (p *lifecycleDialogueProvider) wait(t *testing.T, want int) {
	t.Helper()
	select {
	case got := <-p.started:
		if got != want {
			t.Fatalf("generation=%d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("generation %d never started", want)
	}
}

func TestReplyMergeRetriesAreBoundedAndFinalDraftCoversAcceptedRequests(t *testing.T) {
	withFastSendTiming(t)
	p := &lifecycleDialogueProvider{started: make(chan int, 4), release: make(chan struct{})}
	disabled := false
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	root := directedGroupMessage("root", "user", "解释 stdout")
	done := make(chan error, 1)
	go func() {
		_, err := r.replyAndRecord(context.Background(), root, root.RawMessage, "replied")
		done <- err
	}()
	for i, text := range []string{"补充第一项要求", "改成第二项条件", "第三项需要另答"} {
		p.wait(t, i+1)
		follow := directedGroupMessage("follow-"+text, "user", text)
		_, merged := r.mergeIntoActiveDirectReply(context.Background(), follow, follow.RawMessage)
		if merged != (i < 2) {
			t.Fatalf("round %d merge=%v", i+1, merged)
		}
		p.release <- struct{}{}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("final draft did not finish")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("sent %d drafts", len(sent))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 3 {
		t.Fatalf("unbounded generation: %d", len(p.requests))
	}
	body := requestText(p.requests[2])
	if !strings.Contains(body, "补充第一项要求") || !strings.Contains(body, "改成第二项条件") || strings.Contains(body, "第三项需要另答") {
		t.Fatal("final draft did not match accepted requests")
	}
}

func TestReplyMergeGenerationFailuresRestoreRequest(t *testing.T) {
	for _, failure := range []string{"model error", "model silent", "cancellation", "external write before stale answer"} {
		t.Run(failure, func(t *testing.T) {
			p := &lifecycleDialogueProvider{started: make(chan int, 4), release: make(chan struct{})}
			switch failure {
			case "model error":
				p.failure = errors.New("injected generation failure")
			case "model silent":
				p.failure = newModelSilentFinishError("此轮选择沉默")
			case "external write before stale answer":
				p.sideEffect = true
			}
			disabled := false
			r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, ErrorNotifyEnabled: &disabled, BotReplyLoopDetectionEnabled: &disabled}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
			store := newHandoffInboundStore()
			r.SetInboundEventStore(store)
			root := directedGroupMessage("root", "user", "解释 stdout")
			ctx, cancel := context.WithCancel(withOutboundTurn(context.Background(), "root-turn"))
			defer cancel()
			done := make(chan struct{})
			go func() { _, _ = r.replyAndRecord(ctx, root, root.RawMessage, "replied"); close(done) }()
			p.wait(t, 1)
			follow := directedGroupMessage("follow", "user", "举个例子")
			id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(follow), follow)
			item, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute))
			if !ok {
				t.Fatal("claim failed")
			}
			r.noteSenderTurnInbound(follow, id)
			if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
				t.Fatal("merge rejected")
			}
			if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
				t.Fatal(err)
			}
			if failure == "cancellation" {
				cancel()
			} else {
				p.release <- struct{}{}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("failed turn did not finish")
			}
			if !store.hasPendingRecord(id) {
				t.Fatal("unanswered supplement was lost")
			}
		})
	}
}

func TestReplyMergeIsolatedBySenderSessionAndProfile(t *testing.T) {
	r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
	root := directedGroupMessage("root", "user", "解释 stdout")
	root.ProfileID = "profile-a"
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	for _, dimension := range []string{"sender", "group", "profile", "private"} {
		follow := root
		follow.MessageID = "follow-" + dimension
		switch dimension {
		case "sender":
			follow.UserID = "other"
		case "group":
			follow.GroupID = "other"
		case "profile":
			follow.ProfileID = "profile-b"
		case "private":
			follow.Kind = EventKindPrivate
			follow.GroupID = ""
		}
		if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); merged {
			t.Fatalf("merged across %s", dimension)
		}
	}
}

func TestReplyMergeWaitsForReadableMedia(t *testing.T) {
	r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
	root := directedGroupMessage("root", "user", "解释 stdout")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	voice := voiceEvent("voice", "user", 0, "")
	voice.GroupID = root.GroupID
	if _, merged := r.mergeIntoActiveDirectReply(ctx, voice, "[语音]"); merged {
		t.Fatal("untranscribed voice was consumed")
	}
	photo := photoEvent("photo", "user", 0)
	photo.GroupID, photo.imageLoadErr = root.GroupID, errors.New("image unavailable")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, photo, "这张图呢"); merged {
		t.Fatal("unavailable media was consumed")
	}
	voice.Segments[0].Data[voiceSTTTranscriptKey] = "再举一个例子"
	if _, merged := r.mergeIntoActiveDirectReply(ctx, voice, "再举一个例子"); !merged {
		t.Fatal("readable voice did not participate in merge")
	}
}

// 持久化合并后，队列收尾和根轮次结算可以按任意顺序发生。
// 只有实际包含补充的发送版本会结算，静默、取消、发送失败均放回。
func TestReplyMergeSettlementAndCompletionOrdering(t *testing.T) {
	for _, relation := range []string{"repeat", "supplement", "correction"} {
		for _, delivery := range []string{"silent", "diagnostic", "old draft", "partial answer", "answer", "unconfirmed"} {
			for _, completeFirst := range []bool{false, true} {
				t.Run(relation+"/"+delivery+"/"+map[bool]string{false: "settle first", true: "complete first"}[completeFirst], func(t *testing.T) {
					r := topicTestRuntime(&topicTestProvider{result: `{"relation":"` + relation + `","confidence":0.99}`})
					store := newHandoffInboundStore()
					r.SetInboundEventStore(store)
					root := directedGroupMessage("root", "user", "解释 stdout")
					follow := directedGroupMessage("follow", "user", "举个例子")
					id, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(follow), follow)
					if err != nil {
						t.Fatal(err)
					}
					item, ok, err := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute))
					if err != nil || !ok {
						t.Fatalf("claim=%v err=%v", ok, err)
					}
					r.noteSenderTurnInbound(follow, id)
					r.enterSenderTurnReply(root, true)
					ctx, finish := r.beginDirectReply(withReplyTriggerGate(withOutboundTurn(context.Background(), "root-turn")), root)
					defer finish()
					oldDraft := r.directReplyAttemptContext(ctx)
					if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
						t.Fatal("merge rejected")
					}
					if completeFirst {
						if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
							t.Fatal(err)
						}
					}
					// 巡检不能抢走仍由本进程负责的合并。
					r.sweepInboundHandoffs(context.Background())
					if store.handoffStateOf(follow.MessageID) != "pending" {
						t.Fatal("live merge was released by sweep")
					}
					draft := r.directReplyAttemptContext(ctx)
					switch delivery {
					case "answer":
						r.noteSenderTurnDelivered(draft, root)
						r.noteSenderTurnReplyComplete(root)
					case "old draft":
						r.noteSenderTurnDelivered(oldDraft, root)
						r.noteSenderTurnReplyComplete(root)
					case "partial answer":
						r.noteSenderTurnDelivered(draft, root)
					case "unconfirmed":
						r.noteSenderTurnUnconfirmed(draft, root)
					case "diagnostic":
						r.noteSenderTurnDelivered(withoutCarryOverDelivery(draft), root)
					}
					r.settleSenderBurst(ctx, root)
					if !completeFirst {
						if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
							t.Fatal(err)
						}
					}
					final := delivery == "answer" || delivery == "unconfirmed" || (delivery == "old draft" && relation == "repeat")
					if final {
						if store.handoffStateOf(follow.MessageID) != "final" || store.hasPendingRecord(id) {
							t.Fatal("covered request was requeued")
						}
					} else if !store.hasPendingRecord(id) {
						t.Fatal("unanswered request was lost")
					}
				})
			}
		}
	}
}

type failingMergeStore struct{ *handoffInboundStore }

func (s failingMergeStore) MarkInboundHandoff(context.Context, InboundHandoffRef, string) error {
	return errors.New("storage unavailable")
}

type gatedMergeStore struct {
	*handoffInboundStore
	entered, release chan struct{}
}

func (s *gatedMergeStore) MarkInboundHandoff(ctx context.Context, ref InboundHandoffRef, absorberID string) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.handoffInboundStore.MarkInboundHandoff(ctx, ref, absorberID)
}

func TestReplyMergePersistenceCannotArriveAfterSettlement(t *testing.T) {
	r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
	store := &gatedMergeStore{handoffInboundStore: newHandoffInboundStore(), entered: make(chan struct{}), release: make(chan struct{})}
	r.SetInboundEventStore(store)
	root := directedGroupMessage("root", "user", "解释 stdout")
	follow := directedGroupMessage("follow", "user", "举个例子")
	ctx, finish := r.beginDirectReply(withReplyTriggerGate(context.Background()), root)
	defer finish()
	merged := make(chan bool, 1)
	go func() { _, ok := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); merged <- ok }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("mark did not start")
	}
	settled := make(chan struct{})
	go func() {
		r.noteSenderTurnDelivered(r.directReplyAttemptContext(ctx), root)
		r.noteSenderTurnReplyComplete(root)
		r.settleSenderBurst(ctx, root)
		close(settled)
	}()
	select {
	case <-settled:
		t.Fatal("settlement overtook persistence")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.release)
	if !<-merged {
		t.Fatal("merge failed")
	}
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("settlement stalled")
	}
	if store.handoffStateOf(follow.MessageID) != "final" {
		t.Fatal("late persistence left a delivered merge pending")
	}
}

type lifecycleSendFailureChannel struct {
	*recordingChannel
	err error
}

type lifecyclePartialChannel struct {
	*recordingChannel
	attempts int
}

func (c *lifecyclePartialChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	c.attempts++
	if c.attempts > 1 {
		return nil, &outboundSendError{Cause: errors.New("second chunk rejected"), DeliveryDropped: true}
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestReplyMergePartialDeliveryDoesNotSettleQuestion(t *testing.T) {
	withFastSendTiming(t)
	p := &lifecycleDialogueProvider{started: make(chan int, 4), release: make(chan struct{}), reply: "我分两条说明" + notificationSplitMarker + "这里才是 stdout 和例子的完整解答"}
	channel := &lifecyclePartialChannel{recordingChannel: &recordingChannel{}}
	disabled := false
	r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	store := newHandoffInboundStore()
	r.SetInboundEventStore(store)
	root, follow := privateEvent("user", "root", "解释 stdout"), privateEvent("user", "follow", "举个例子")
	done := make(chan struct{})
	go func() { _, _ = r.replyAndRecord(context.Background(), root, root.RawMessage, "replied"); close(done) }()
	p.wait(t, 1)
	id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(follow), follow)
	item, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("claim failed")
	}
	r.noteSenderTurnInbound(follow, id)
	if _, merged := r.mergeIntoActiveDirectReply(context.Background(), follow, follow.RawMessage); !merged {
		t.Fatal("merge rejected")
	}
	if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
		t.Fatal(err)
	}
	p.release <- struct{}{}
	p.wait(t, 2)
	p.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("partial send did not finish")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "我分两条说明" {
		t.Fatalf("unexpected partial delivery: %#v", sent)
	}
	if !store.hasPendingRecord(id) {
		t.Fatal("first chunk acknowledged but unanswered question lost")
	}
}

func (c lifecycleSendFailureChannel) SendWithResult(context.Context, OutgoingMessage) (map[string]any, error) {
	return nil, c.err
}

func TestReplyMergeSendFailureAndAmbiguousOutcome(t *testing.T) {
	withFastSendTiming(t)
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "unconfirmed"}[ambiguous], func(t *testing.T) {
			r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
			failure := &outboundSendError{Cause: errors.New("injected rejection"), DeliveryDropped: true}
			if ambiguous {
				failure.Cause, failure.OutcomeUnconfirmed = errOutboundOutcomeUnconfirmed, true
			}
			r.channel = lifecycleSendFailureChannel{recordingChannel: &recordingChannel{}, err: failure}
			store := newHandoffInboundStore()
			r.SetInboundEventStore(store)
			root, follow := privateEvent("user", "root", "解释 stdout"), privateEvent("user", "follow", "举个例子")
			id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(follow), follow)
			item, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute))
			if !ok {
				t.Fatal("claim failed")
			}
			r.noteSenderTurnInbound(follow, id)
			ctx, finish := r.beginDirectReply(withReplyTriggerGate(withOutboundTurn(context.Background(), "root-turn")), root)
			defer finish()
			if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
				t.Fatal("merge rejected")
			}
			if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
				t.Fatal(err)
			}
			sendCtx, cancel := context.WithTimeout(r.directReplyAttemptContext(ctx), time.Second)
			defer cancel()
			_, err := r.sendOutgoingWithResult(sendCtx, root, OutgoingMessage{Text: "stdout 和例子一起回答"})
			if !errors.Is(err, failure) {
				t.Fatalf("send err=%v", err)
			}
			r.settleSenderBurst(ctx, root)
			if store.hasPendingRecord(id) == ambiguous {
				t.Fatalf("ambiguous=%v requeued=%v", ambiguous, store.hasPendingRecord(id))
			}
		})
	}
}

func TestReplyMergePersistenceFailureKeepsRequestIndependent(t *testing.T) {
	r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
	r.SetInboundEventStore(failingMergeStore{newHandoffInboundStore()})
	root := directedGroupMessage("root", "user", "解释 stdout")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	follow := directedGroupMessage("follow", "user", "举个例子")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); merged {
		t.Fatal("persist failure consumed the request")
	}
	if _, owned := r.senderTurnSupersededBy(follow); owned || len(r.directReplySupplements(ctx)) != 0 {
		t.Fatal("persist failure left an in-memory owner")
	}
}

func TestReplyMergeRestartRecoversPendingRequest(t *testing.T) {
	r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
	store := newHandoffInboundStore()
	r.SetInboundEventStore(store)
	root := directedGroupMessage("root", "user", "解释 stdout")
	follow := directedGroupMessage("follow", "user", "举个例子")
	id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(follow), follow)
	item, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("claim failed")
	}
	r.noteSenderTurnInbound(follow, id)
	ctx, finish := r.beginDirectReply(withOutboundTurn(context.Background(), "root-turn"), root)
	defer finish()
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("merge rejected")
	}
	if err := r.completeHandedOffInbound(context.Background(), store, item, "worker"); err != nil {
		t.Fatal(err)
	}
	restarted := topicTestRuntime(nil)
	restarted.SetInboundEventStore(store)
	restarted.sweepInboundHandoffs(context.Background())
	if !store.hasPendingRecord(id) {
		t.Fatal("restart lost merged request")
	}
}

func TestReplyMergeClosesOnSideEffectAndFinalSendGate(t *testing.T) {
	for _, boundary := range []string{"send gate", "external write", "finish"} {
		t.Run(boundary, func(t *testing.T) {
			r := topicTestRuntime(&topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`})
			root := directedGroupMessage("root", "user", "解释 stdout")
			ctx, finish := r.beginDirectReply(withExternalSideEffectLedger(withReplyTriggerGate(context.Background())), root)
			defer finish()
			onExternalSideEffect(ctx, func() { r.sealDirectReply(ctx) })
			switch boundary {
			case "send gate":
				if r.directReplyHasNewSupplements(r.directReplyAttemptContext(ctx)) {
					t.Fatal("empty run has a supplement")
				}
			case "external write":
				markExternalSideEffect(ctx)
			case "finish":
				finish()
			}
			follow := directedGroupMessage("follow", "user", "举个例子")
			if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); merged {
				t.Fatal("request consumed after " + boundary)
			}
		})
	}
}

type lifecycleResolverPlugin struct{ duplicateResolverPlugin }

func (lifecycleResolverPlugin) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	if !hasKnownResolverPlatformURL(req.Event, req.Text) {
		return nil, nil
	}
	return &PluginResponse{Handled: true, Reply: "链接已解析：Mujica 总集篇", FollowUp: true}, nil
}

// 没有声明 DirectTriggerPlugin 的插件也可能返回固定结果；准入必须等实际路径确定。
type lifecycleStaticPlugin struct {
	entered, release chan struct{}
}

func (p lifecycleStaticPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "lifecycle-static", BuiltIn: true, Name: "static response"}
}

func (p lifecycleStaticPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	if req.Event.MessageID != "root" {
		return nil, nil
	}
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &PluginResponse{Handled: true, Reply: "固定插件结果"}, nil
}

func TestReplyMergeDoesNotOpenBeforePluginDispatchFinishes(t *testing.T) {
	withFastSendTiming(t)
	plugin := lifecycleStaticPlugin{entered: make(chan struct{}), release: make(chan struct{})}
	p := &lifecycleFollowUpProvider{}
	disabled := false
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled}, channel, NewPluginManager(plugin), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	root := directedGroupMessage("root", "user", "查一下这个")
	r.noteDirectedInbound(root)
	done := make(chan error, 1)
	go func() {
		_, err := r.replyAndRecord(context.Background(), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-plugin.entered:
	case <-time.After(time.Second):
		t.Fatal("plugin did not start")
	}
	follow := directedGroupMessage("follow", "user", "还有个问题需要解释")
	r.noteDirectedInbound(follow)
	if _, merged := r.mergeIntoActiveDirectReply(context.Background(), follow, follow.RawMessage); merged {
		t.Fatal("unresolved plugin path accepted a question")
	}
	if _, err := r.replyAndRecord(context.Background(), follow, follow.RawMessage, "replied"); err != nil {
		t.Fatal(err)
	}
	close(plugin.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin result stalled")
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || sent[1].Text != "固定插件结果" {
		t.Fatalf("fixed result or dialogue lost: %#v", sent)
	}
}

type lifecycleFollowUpProvider struct {
	started, release chan struct{}
}

func (p *lifecycleFollowUpProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	if strings.Contains(body, "你刚刚把下面这条内容发到了这个会话里") {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &llm.GenerateResponse{Text: "旧的自然跟评"}, nil
	}
	if strings.Contains(body, "发送前审核器") {
		return &llm.GenerateResponse{Text: `{"send_confidence":0.99,"account_safe":true}`}, nil
	}
	if strings.Contains(body, "action") && strings.Contains(body, "prompt") {
		return &llm.GenerateResponse{Text: `{"action":"none","prompt":""}`}, nil
	}
	return &llm.GenerateResponse{Text: "与 TV 的区别：电影是重新剪辑的总集篇。"}, nil
}

// 完整插件链路：事实投递不能被追问取消；可选跟评不会吞追问，也不会在追问后抢答。
func TestResolverFollowUpLeavesNewQuestionToDialogue(t *testing.T) {
	withFastSendTiming(t)
	p := &lifecycleFollowUpProvider{started: make(chan struct{}), release: make(chan struct{})}
	channel := &recordingChannel{}
	disabled := false
	r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled}, channel, NewPluginManager(lifecycleResolverPlugin{}), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	root := directedGroupMessage("root", "user", "https://x.com/example/status/123456789")
	r.noteDirectedInbound(root)
	done := make(chan error, 1)
	go func() {
		_, err := r.replyAndRecord(context.Background(), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up did not start")
	}
	follow := directedGroupMessage("follow", "user", "这个和 TV 版什么区别")
	r.noteDirectedInbound(follow)
	prepared, text, handled, outcome := r.prepareMessageEvent(context.Background(), follow)
	if !handled || outcome == "merged_into_reply" {
		t.Fatalf("new question handled=%v outcome=%q", handled, outcome)
	}
	if _, err := r.replyAndRecord(context.Background(), prepared, text, "replied"); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolver did not finish")
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || !strings.Contains(sent[0].Text, "链接已解析") || !strings.Contains(sent[1].Text, "与 TV 的区别") {
		t.Fatalf("lost question or sent obsolete commentary: %#v", sent)
	}
}
