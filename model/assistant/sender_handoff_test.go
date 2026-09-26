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

// 连发交接（sender_burst.go、inbound_handoff.go）的回归用例：交出去的一轮从不等待，
// 接手那一轮回没回出去决定交接落定还是撤销，重启后待定的交接由巡检放回。

// 交接不等待：前一条被接走后立刻收尾；后一条没回出去（这里是生成出错），前一条
// 被放回去自己回答，而且只回一次；后一条回出去了，前一条就此落定。
func TestSenderBurstHandoffSettlesByAbsorberResult(t *testing.T) {
	for _, tc := range []struct {
		name      string
		laterErr  error
		wantSends int
		wantFinal bool
	}{
		// 后一条出错时只发了一句错误说明，那不是对前一条的回答：前一条重新回答。
		{name: "absorber fails", laterErr: errors.New("model down"), wantSends: 2, wantFinal: false},
		{name: "absorber replies", laterErr: nil, wantSends: 1, wantFinal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &gatedBurstProvider{entered: make(chan struct{}), release: make(chan error, 1)}
			runtime, channel := burstTestRuntime(provider)
			first := directedGroupMessage("20111", "10001", "帮我看看这个报错")
			second := directedGroupMessage("20112", "10001", "就是登录那个")
			first.Time, second.Time = 1_800_000_000, 1_800_000_003
			arriveTogether(runtime, first, second)

			secondDone := make(chan string, 1)
			go func() {
				outcome, _ := runtime.replyAndRecord(context.Background(), second, "就是登录那个", "replied")
				secondDone <- outcome
			}()
			select {
			case <-provider.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("later turn never started generating")
			}
			// 前一条这时走到回复入口：不等后一条，立刻交出去。
			start := time.Now()
			outcome, err := runtime.replyAndRecord(context.Background(), first, "帮我看看这个报错", "replied")
			if err != nil || outcome != inboundOutcomeHandedOffPending {
				t.Fatalf("earlier outcome=%q err=%v", outcome, err)
			}
			if waited := time.Since(start); waited > time.Second {
				t.Fatalf("earlier turn waited %s for the absorber", waited)
			}
			provider.release <- tc.laterErr
			<-secondDone
			if tc.wantFinal {
				if by, ok := runtime.senderTurnSupersededBy(first); !ok || by != "20112" {
					t.Fatalf("handoff should be final, got %q %v", by, ok)
				}
			}
			waitForCondition(t, 5*time.Second, func() bool { return len(channel.sentSnapshot()) >= tc.wantSends })
			// 再等一会儿，确认不会多出一条。
			time.Sleep(200 * time.Millisecond)
			if sent := channel.sentSnapshot(); len(sent) != tc.wantSends {
				t.Fatalf("sends=%d want %d: %#v", len(sent), tc.wantSends, sent)
			}
			if !tc.wantFinal && !provider.sawPrompt("帮我看看这个报错") {
				t.Fatal("released earlier message never reached the model")
			}
		})
	}
}

// N1：被接走的那一轮在发送闸门上只看状态、从不等待——它这时握着这个人的发送锁，
// 等下去接手那一轮就发不出去。它必须立刻让出，接手那一轮照常发，最后只有一条回复。
func TestSenderBurstGateNeverWaitsUnderOutboundLock(t *testing.T) {
	photoGate := make(chan struct{})
	questionEntered := make(chan struct{})
	questionGate := make(chan struct{})
	photoEntered := make(chan struct{})
	var questionOnce, photoOnce sync.Once
	provider := &scriptedReplyProvider{description: "一只橘猫", reply: func(req llm.GenerateRequest) string {
		// 文字那一轮带着候选依赖图块，图那一轮没有。
		if strings.Contains(requestText(req), "【同一发言者稍早发的图") {
			questionOnce.Do(func() { close(questionEntered) })
			<-questionGate
			return "这是一只橘猫"
		}
		// 图那一轮先开始生成，那时历史里还没有文字那条。
		if !strings.Contains(requestText(req), "橘猫叫什么名字") {
			photoOnce.Do(func() { close(photoEntered) })
			<-photoGate
			return "好可爱的猫"
		}
		return "好的"
	}}
	runtime, question := dependencyTestRuntime(t, provider, false, nil)
	question.RawMessage = "橘猫叫什么名字"
	question.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": question.RawMessage}}}
	photo := historyEventByID(t, runtime, question, "photo-1")
	runtime.noteSenderTurnArrival(photo)
	runtime.noteSenderTurnArrival(question)

	photoDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), photo, "", "replied")
		photoDone <- outcome
	}()
	select {
	case <-photoEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("image turn never started generating")
	}
	// 预处理阶段已经拿图那一轮做过话题判断（这里不走预处理），回复入口不再重复判，
	// 文字那一轮直接生成，把图当候选依赖图接过来。
	runtime.noteSenderTurnMergeChecked(question, "photo-1", true)
	questionDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), question, question.RawMessage, "replied")
		questionDone <- outcome
	}()
	select {
	case <-questionEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("question turn never started generating")
	}
	// 图那一轮这时生成完、带着发送锁走到闸门：必须立刻让出，不能等文字那一轮。
	close(photoGate)
	select {
	case outcome := <-photoDone:
		if outcome != inboundOutcomeHandedOffPending {
			t.Fatalf("image turn outcome=%q", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("image turn blocked at the send gate")
	}
	close(questionGate)
	select {
	case outcome := <-questionDone:
		if outcome != "replied" {
			t.Fatalf("question outcome=%q", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("question turn never finished")
	}
	time.Sleep(100 * time.Millisecond)
	if sent := nonEmptySends(runtime); sent != 1 {
		t.Fatalf("image then text should produce exactly one reply, got %d", sent)
	}
}

// N2：主人命令这类不经过模型生成的回复不会接走前一条。
func TestOwnerCommandDoesNotAbsorbEarlierQuestion(t *testing.T) {
	provider := &burstReplyProvider{}
	disabled := false
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001", GroupTriggers: []string{"Diana"}, BotReplyLoopDetectionEnabled: &disabled},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	first := privateEvent("10001", "20161", "明天几点开会")
	command := privateEvent("10001", "20162", "群 列表")
	first.Time, command.Time = 1_800_000_000, 1_800_000_002
	arriveTogether(runtime, first, command)

	if outcome, err := runtime.replyAndRecord(context.Background(), command, "群 列表", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("owner command outcome=%q err=%v", outcome, err)
	}
	if _, superseded := runtime.senderTurnSupersededBy(first); superseded {
		t.Fatal("an owner command must not take over the earlier question")
	}
	if provider.sawPrompt("明天几点开会") {
		t.Fatal("the owner command turn should not have gone through the model at all")
	}
	if outcome, err := runtime.replyAndRecord(context.Background(), first, "明天几点开会", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("earlier question must still be answered, outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 {
		t.Fatalf("command reply plus the question's own answer, got %#v", sent)
	}
}

// N3：只从旧往新接。早的文字不会把晚到的图当依赖图接走；两张图之间只能是晚的接早的。
func TestSenderBurstOnlyAbsorbsOlderMessages(t *testing.T) {
	provider := &scriptedReplyProvider{description: "一只橘猫"}
	runtime, base := dependencyTestRuntime(t, provider, false, nil)
	photo := historyEventByID(t, runtime, base, "photo-1")
	text := base
	text.MessageID, text.Time = "t-early", photo.Time+20
	later := photo
	later.MessageID, later.Time = "photo-late", photo.Time+30
	runtime.noteSenderTurnArrival(text)
	arriveReady(runtime, text)
	runtime.noteSenderTurnArrival(later)
	arriveReady(runtime, later)
	runtime.enterSenderTurnReply(later, true)

	if outcome, err := runtime.replyAndRecord(context.Background(), text, text.RawMessage, "replied"); err != nil || outcome != "replied" {
		t.Fatalf("text outcome=%q err=%v", outcome, err)
	}
	if _, superseded := runtime.senderTurnSupersededBy(later); superseded {
		t.Fatal("an earlier text must not take over a later image")
	}

	// 两张图：晚的可以接早的，早的不能接晚的，不会互相接走。
	older := photoEvent("20171", "10001", 1_800_000_000)
	newer := photoEvent("20172", "10001", 1_800_000_004)
	for _, image := range []MessageEvent{older, newer} {
		runtime.noteSenderTurnArrival(image)
		runtime.enterSenderTurnReply(image, true)
	}
	runtime.supersedeDependencyImageTurns(context.Background(), older, []senderDependencyImage{{Source: newer}})
	if _, superseded := runtime.senderTurnSupersededBy(newer); superseded {
		t.Fatal("an older image took over a newer one")
	}
	runtime.supersedeDependencyImageTurns(context.Background(), newer, []senderDependencyImage{{Source: older}})
	if by, superseded := runtime.senderTurnSupersededBy(older); !superseded || by != "20172" {
		t.Fatalf("the newer image should take over the older one, got %q %v", by, superseded)
	}
	runtime.supersedeDependencyImageTurns(context.Background(), older, []senderDependencyImage{{Source: newer}})
	if _, superseded := runtime.senderTurnSupersededBy(newer); superseded {
		t.Fatal("handoff cycle between two images")
	}
}

// 已经交给别的轮次的消息，第三条不再点名，也不再接。
func TestCarryOverSkipsMessagesAlreadyHandedOff(t *testing.T) {
	runtime, _ := burstTestRuntime(&burstReplyProvider{})
	a := directedGroupMessage("20181", "10001", "第一句")
	b := directedGroupMessage("20182", "10001", "第二句")
	c := directedGroupMessage("20183", "10001", "第三句")
	a.Time, b.Time, c.Time = 1_800_000_000, 1_800_000_002, 1_800_000_004
	arriveTogether(runtime, a, b, c)
	runtime.enterSenderTurnReply(b, true)
	if carry := runtime.claimCarryOver(context.Background(), b, runtime.contextHistory(b)); len(carry) != 1 || carry[0].MessageID != "20181" {
		t.Fatalf("b should take over a: %#v", carry)
	}
	runtime.enterSenderTurnReply(c, true)
	if carry := runtime.claimCarryOver(context.Background(), c, runtime.contextHistory(c)); len(carry) != 0 {
		t.Fatalf("c must not mention a (taken by b) or b (answering itself): %#v", carry)
	}
}

// handoffInboundStore 在内存队列上补齐交接持久化，语义和 SQLite 实现一致。
type handoffInboundStore struct {
	*memoryInboundEventStore
	states map[string]string
	byID   int
}

func newHandoffInboundStore() *handoffInboundStore {
	return &handoffInboundStore{memoryInboundEventStore: newMemoryInboundEventStore(), states: map[string]string{}}
}

func (s *handoffInboundStore) recordByMessageLocked(messageID string) *memoryInboundRecord {
	for _, record := range s.records {
		if record.item.Event.MessageID == messageID {
			return record
		}
	}
	return nil
}

func (s *handoffInboundStore) handoffStateOf(messageID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[messageID]
}

func (s *handoffInboundStore) hasPendingRecord(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id]
	return record != nil && record.state == "pending"
}

func (s *handoffInboundStore) priorityOf(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[id]; record != nil {
		return record.item.Priority
	}
	return 0
}

// messageIDLocked 按主键或消息 ID 定位；记下按主键找到的次数，验证热路径走的是主键。
func (s *handoffInboundStore) messageIDLocked(ref InboundHandoffRef) string {
	if record := s.records[ref.ID]; ref.ID != "" && record != nil {
		s.byID++
		return record.item.Event.MessageID
	}
	return ref.Event.MessageID
}

func (s *handoffInboundStore) MarkInboundHandoff(_ context.Context, ref InboundHandoffRef, absorberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID := s.messageIDLocked(ref)
	if s.superseded[messageID] == "" {
		s.superseded[messageID], s.states[messageID] = absorberID, "pending"
	}
	return nil
}

func (s *handoffInboundStore) FinalizeInboundHandoff(_ context.Context, ref InboundHandoffRef, absorberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID := s.messageIDLocked(ref)
	if s.superseded[messageID] == absorberID && s.states[messageID] == "pending" {
		s.states[messageID] = "final"
		if record := s.recordByMessageLocked(messageID); record != nil && record.state == "done" && record.outcome == inboundOutcomeHandedOffPending {
			record.outcome = "superseded_follow_up"
		}
	}
	return nil
}

func (s *handoffInboundStore) ReleaseInboundHandoff(_ context.Context, ref InboundHandoffRef, absorberID string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID := s.messageIDLocked(ref)
	if s.superseded[messageID] != absorberID || s.states[messageID] != "pending" {
		return false, false, nil
	}
	delete(s.superseded, messageID)
	delete(s.states, messageID)
	record := s.recordByMessageLocked(messageID)
	if record == nil || record.state != "done" || record.outcome != inboundOutcomeHandedOffPending {
		return true, false, nil
	}
	record.state, record.outcome = "pending", ""
	if record.item.Priority < InboundPriorityTriggered {
		record.item.Priority = InboundPriorityTriggered
	}
	return true, true, nil
}

func (s *handoffInboundStore) CompleteInboundHandoff(_ context.Context, id, leaseOwner, absorberID string, final bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id]
	if record == nil || record.state != "processing" || record.leaseOwner != leaseOwner {
		return nil
	}
	messageID := record.item.Event.MessageID
	record.state, record.leaseOwner, record.outcome = "done", "", inboundOutcomeHandedOffPending
	state := "pending"
	// 落定抢在收尾前面时不改回待定。
	if final || (s.states[messageID] == "final" && s.superseded[messageID] == absorberID) {
		record.outcome, state = "superseded_follow_up", "final"
	}
	if absorberID != "" {
		s.superseded[messageID] = absorberID
	}
	s.states[messageID] = state
	return nil
}

func (s *handoffInboundStore) ListPendingInboundHandoffs(_ context.Context, _ int) ([]InboundHandoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending []InboundHandoff
	for messageID, state := range s.states {
		if state != "pending" {
			continue
		}
		if record := s.recordByMessageLocked(messageID); record != nil {
			pending = append(pending, InboundHandoff{InboundHandoffRef: InboundHandoffRef{ID: record.item.ID, Event: record.item.Event}, AbsorberID: s.superseded[messageID]})
		}
	}
	return pending, nil
}

// 重启恢复：上一个进程留下的待定交接没有在跑的接手轮次，巡检把它放回队列，
// 优先级提到直接触发那一档。
func TestInboundHandoffSweepRequeuesOrphanedHandoff(t *testing.T) {
	store := newHandoffInboundStore()
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	event := directedGroupMessage("20191", "10001", "帮我看看")
	id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	if _, ok, _ := store.ClaimNextInboundEvent(context.Background(), "old-process", time.Now().Add(time.Minute)); !ok {
		t.Fatal("claim failed")
	}
	if err := store.CompleteInboundHandoff(context.Background(), id, "old-process", "absorber-turn", false); err != nil {
		t.Fatal(err)
	}

	runtime.sweepInboundHandoffs(context.Background())
	if !store.hasPendingRecord(id) {
		t.Fatal("orphaned handoff should be requeued")
	}
	if _, superseded, _ := store.InboundEventSuperseded(context.Background(), event); superseded {
		t.Fatal("released handoff must clear superseded_by")
	}
	if priority := store.priorityOf(id); priority < InboundPriorityTriggered {
		t.Fatalf("requeued handoff priority=%d", priority)
	}
}

// 接手那一轮跑得再久（这里卡在生成里）也不会有第二份回答：巡检不碰还在跑的接手轮次，
// 它回出去之后交接落定，前一条不会被重新排队。
func TestLongAbsorberDoesNotCauseDuplicate(t *testing.T) {
	provider := &gatedBurstProvider{entered: make(chan struct{}), release: make(chan error, 1)}
	runtime, channel := burstTestRuntime(provider)
	store := newHandoffInboundStore()
	runtime.SetInboundEventStore(store)
	first := directedGroupMessage("20201", "10001", "帮我看看这个报错")
	second := directedGroupMessage("20202", "10001", "就是登录那个")
	first.Time, second.Time = 1_800_000_000, 1_800_000_003
	firstID, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(first), first)
	arriveTogether(runtime, first, second)
	runtime.noteSenderTurnInbound(first, firstID)

	secondDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(withOutboundTurn(context.Background(), "turn-second"), second, "就是登录那个", "replied")
		secondDone <- outcome
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("absorber never started generating")
	}
	// 前一条在队列里走完：交出去、落终态。
	if _, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute)); !ok {
		t.Fatal("claim failed")
	}
	if outcome, _ := runtime.replyAndRecord(context.Background(), first, "帮我看看这个报错", "replied"); outcome != inboundOutcomeHandedOffPending {
		t.Fatalf("earlier outcome=%q", outcome)
	}
	if err := runtime.completeHandedOffInbound(context.Background(), store, InboundQueueItem{ID: firstID, Event: first}, "worker"); err != nil {
		t.Fatal(err)
	}
	// 接手那一轮还在跑：巡检不能把前一条放回去。
	runtime.sweepInboundHandoffs(context.Background())
	if store.hasPendingRecord(firstID) {
		t.Fatal("sweep released a handoff whose absorber is still running")
	}
	provider.release <- nil
	<-secondDone
	if state := store.handoffStateOf("20201"); state != "final" {
		t.Fatalf("handoff state=%q, want final", state)
	}
	runtime.sweepInboundHandoffs(context.Background())
	if store.hasPendingRecord(firstID) {
		t.Fatal("a finalized handoff must never be requeued")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("sends=%#v", sent)
	}
}
