// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func consecutiveTestEvent(userID string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: userID}
}

// 截图里的那一幕：同一个人连发「内容是什么？」和「@Diana」，两轮并发生成，
// 第二轮开跑时第一轮的答案还没写进历史，于是把同一件事完整答了两遍。
func TestBeginReplyTurnSeesAnInFlightTurnFromTheSameSpeaker(t *testing.T) {
	runtime := &Runtime{}
	event := consecutiveTestEvent("10001")
	now := time.Now()

	if _, ok := runtime.beginReplyTurn(event, now); ok {
		t.Fatal("第一轮不该看到上一轮")
	}
	previous, ok := runtime.beginReplyTurn(event, now.Add(2*time.Second))
	if !ok {
		t.Fatal("紧接着的第二轮没有看到上一轮")
	}
	if !previous.inFlight() {
		t.Fatalf("上一轮还没答完，应当标成 in-flight: %#v", previous)
	}
	block := consecutiveReplyContext(previous)
	if !strings.Contains(block, "正在回答") {
		t.Fatalf("并发那一路的提示词没说清状态: %q", block)
	}
	if !strings.Contains(block, "不要再说一遍") {
		t.Fatalf("提示词缺少「别重说」这条约束: %q", block)
	}
}

// 上一轮已经答完时只传状态，不再重复注入历史中的答案。
func TestBeginReplyTurnCarriesFinishedStatusWithoutReplyExcerpt(t *testing.T) {
	runtime := &Runtime{}
	event := consecutiveTestEvent("10001")
	now := time.Now()

	runtime.beginReplyTurn(event, now)
	runtime.finishReplyTurn(event, "珂珂刚才撤回的内容是「海王」喵", now.Add(time.Second))

	previous, ok := runtime.beginReplyTurn(event, now.Add(3*time.Second))
	if !ok {
		t.Fatal("第二轮没有看到上一轮")
	}
	if previous.inFlight() {
		t.Fatalf("上一轮已经答完，不该还是 in-flight: %#v", previous)
	}
	block := consecutiveReplyContext(previous)
	if strings.Contains(block, "海王") {
		t.Fatalf("上一轮答案不应再次注入: %q", block)
	}
	if !strings.Contains(block, "已经回答过") {
		t.Fatalf("提示词 = %q", block)
	}
}

// 过了时间窗就是新问题，不该被要求从简。
func TestBeginReplyTurnIgnoresStaleTurns(t *testing.T) {
	runtime := &Runtime{}
	event := consecutiveTestEvent("10001")
	now := time.Now()

	runtime.beginReplyTurn(event, now)
	runtime.finishReplyTurn(event, "上一轮说过的话", now.Add(time.Second))

	if _, ok := runtime.beginReplyTurn(event, now.Add(consecutiveReplyWindow+time.Second)); ok {
		t.Fatal("超出时间窗的上一轮仍然被算作「刚问过」")
	}
	// 过期痕迹要顺手清掉，不能靠「下次被读到」才删——不再开口的人会一直挂着。
	runtime.replyTurnMu.Lock()
	remaining := len(runtime.replyTurns)
	runtime.replyTurnMu.Unlock()
	if remaining != 1 {
		t.Fatalf("过期痕迹没有被清理，剩余 %d 条", remaining)
	}
}

// 群里两个人先后问不同的问题，本来就该各答各的。
func TestBeginReplyTurnDoesNotCrossSpeakers(t *testing.T) {
	runtime := &Runtime{}
	now := time.Now()

	runtime.beginReplyTurn(consecutiveTestEvent("10001"), now)
	if _, ok := runtime.beginReplyTurn(consecutiveTestEvent("10002"), now.Add(time.Second)); ok {
		t.Fatal("另一个人的轮次被算作了同一个人的连续消息")
	}
}

// 认不出发言人时不记：那会退回一个人人都命中的公共桶，
// 反而让不相干的两个人互相压制。
func TestBeginReplyTurnSkipsAnonymousEvents(t *testing.T) {
	runtime := &Runtime{}
	anonymous := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	now := time.Now()

	runtime.beginReplyTurn(anonymous, now)
	if _, ok := runtime.beginReplyTurn(anonymous, now.Add(time.Second)); ok {
		t.Fatal("没有发言人身份的事件被记进了痕迹表")
	}
	runtime.replyTurnMu.Lock()
	tracked := len(runtime.replyTurns)
	runtime.replyTurnMu.Unlock()
	if tracked != 0 {
		t.Fatalf("匿名事件被记了 %d 条痕迹", tracked)
	}
}

// 这一轮最后没开口时不该留下「刚答过」：下一条消息会被无故要求从简。
func TestFinishReplyTurnIgnoresEmptyReplies(t *testing.T) {
	runtime := &Runtime{}
	event := consecutiveTestEvent("10001")
	now := time.Now()

	runtime.beginReplyTurn(event, now)
	runtime.finishReplyTurn(event, "   ", now.Add(time.Second))

	previous, ok := runtime.beginReplyTurn(event, now.Add(2*time.Second))
	if !ok {
		t.Fatal("同一时间窗内的上一轮应当仍然可见")
	}
	// 没说出口的轮次留在 in-flight 状态即可，但不能凭空多出一段「你说过的话」。
	if !previous.inFlight() {
		t.Fatalf("空回复不应标记为已发送: %#v", previous)
	}
}

// 提示词要留出「这确实是另一件事」的余地，否则连续两个不同问题的第二个会被压成一句话。
func TestConsecutiveReplyContextAllowsGenuineNewTopics(t *testing.T) {
	block := consecutiveReplyContext(replyTurnRecord{startedAt: time.Now(), finishedAt: time.Now()})
	if !strings.Contains(block, "另一件事") {
		t.Fatalf("提示词把所有后续消息都当成了追问: %q", block)
	}
}

// 并发登记不能打架：这条路径本来就是两轮同时开跑。
func TestBeginReplyTurnIsConcurrencySafe(t *testing.T) {
	runtime := &Runtime{}
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			event := consecutiveTestEvent("1000" + string(rune('0'+index%4)))
			runtime.beginReplyTurn(event, now)
			runtime.finishReplyTurn(event, "答案", now)
		}(i)
	}
	wg.Wait()
	runtime.replyTurnMu.Lock()
	tracked := len(runtime.replyTurns)
	runtime.replyTurnMu.Unlock()
	if tracked != 4 {
		t.Fatalf("并发登记后的痕迹数 = %d, want 4", tracked)
	}
}
