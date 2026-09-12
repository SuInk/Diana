package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type semanticGateProvider struct {
	mu       sync.Mutex
	result   string
	err      error
	requests []llm.GenerateRequest
	audits   []string
	onJudge  func()
}

func (p *semanticGateProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range req.Messages {
		if strings.Contains(m.Content, semanticReplyPrompt) {
			p.requests = append(p.requests, req)
			if p.onJudge != nil {
				p.onJudge()
			}
			return &llm.GenerateResponse{Text: p.result}, p.err
		}
		if strings.Contains(m.Content, "你是机器人回复的发送前审核器") {
			p.audits = append(p.audits, req.Messages[len(req.Messages)-1].Content)
			return &llm.GenerateResponse{Text: `{"send_confidence":0.99,"account_safe":true}`}, nil
		}
	}
	return &llm.GenerateResponse{Text: "原有说明和新增信息"}, nil
}

func TestSemanticReplyDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, result, want string
		err                error
		drop               bool
	}{
		{"keep", `{"action":"keep","confidence":0.99,"content":"不应采用"}`, "原有说明和新增信息", nil, false},
		{"drop", `{"action":"drop","confidence":0.99}`, "", nil, true},
		{"rewrite", `{"action":"rewrite","confidence":0.99,"content":"新增信息"}`, "新增信息", nil, false},
		{"low", `{"action":"drop","confidence":0.5}`, "原有说明和新增信息", nil, false},
		{"empty", `{"action":"rewrite","confidence":0.99,"content":""}`, "原有说明和新增信息", nil, false},
		{"control", `{"action":"rewrite","confidence":0.99,"content":"[[DIANA_REPLY_SINGLE]]改写"}`, "原有说明和新增信息", nil, false},
		{"invalid", `broken`, "原有说明和新增信息", nil, false},
		{"timeout", "", "原有说明和新增信息", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &semanticGateProvider{result: tc.result, err: tc.err}
			r := topicTestRuntime(p)
			event := directedGroupMessage("m", "u", "新的问题")
			g, release, err := r.lockSemanticReply(context.Background(), event)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			g.remember("原问题", "原有说明")
			event.replyDeliveryMode = replyDeliverySingle
			got, err := r.deduplicateReply(context.Background(), event, "新的问题", "原有说明和新增信息", BotConfig{MaxReplyChars: 300}, g, true)
			if got != tc.want || errors.Is(err, errDuplicateReply) != tc.drop {
				t.Fatalf("got=%q err=%v", got, err)
			}
			if len(p.requests) != 1 {
				t.Fatalf("calls=%d", len(p.requests))
			}
		})
	}
}

func TestSemanticReplyWaitsForSuccessfulDelivery(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"drop","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("one", "u", "问题")
	g, release, err := r.lockSemanticReply(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *semanticReplyGate, 1)
	go func() { next, unlock, _ := r.lockSemanticReply(context.Background(), root); done <- next; unlock() }()
	select {
	case <-done:
		t.Fatal("second turn passed before first delivery")
	case <-time.After(20 * time.Millisecond):
	}
	g.remember("问题", "已成功发送")
	release()
	select {
	case next := <-done:
		if len(next.sent) != 1 {
			t.Fatal("missing acknowledged reply")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked")
	}
	other := root
	other.GroupID = "other"
	g, release, err = r.lockSemanticReply(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(g.sent) != 0 {
		t.Fatal("leaked across sessions")
	}
	if got, err := r.deduplicateReply(context.Background(), other, "问题", "新内容", BotConfig{}, g, true); got != "新内容" || err != nil || len(p.requests) != 0 {
		t.Fatalf("empty history used model: %q %v", got, err)
	}
}

// proactiveGroupMessage 是主动接话那一侧的同类事件：没有 @ 本机，由主动路由挑中。
// 语义去重在两条路径上的结论不同，所以两边都要有事件构造器。
func proactiveGroupMessage(messageID, userID, text string) MessageEvent {
	return MessageEvent{
		Kind:           EventKindGroup,
		GroupID:        "123456",
		UserID:         userID,
		MessageID:      messageID,
		RawMessage:     text,
		Segments:       []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		proactiveReply: true,
	}
}

func TestSemanticReplyRuntimeDropAndRewrite(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		proactive    bool
		// wantSilent 表示这一轮什么都不发。只有主动接话允许这样：那里沉默本来
		// 就是默认行为。直接触发是对方点着名在说话，静默等于装死。
		wantSilent bool
	}{
		{"proactive_drop", "drop", true, true},
		{"proactive_rewrite", "rewrite", true, false},
		{"direct_drop", "drop", false, false},
		{"direct_rewrite", "rewrite", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &semanticGateProvider{result: `{"action":"` + tc.action + `","confidence":0.99,"content":"新增信息"}`}
			r := topicTestRuntime(p)
			build := directedGroupMessage
			if tc.proactive {
				build = proactiveGroupMessage
			}
			root := build("first", "u", "原问题")
			if _, err := r.replyAndRecord(context.Background(), root, root.RawMessage, "replied"); err != nil {
				t.Fatal(err)
			}
			follow := build("second", "u", "后来的问题")
			outcome, err := r.replyAndRecord(context.Background(), follow, follow.RawMessage, "replied")
			if err != nil {
				t.Fatal(err)
			}
			sent := r.channel.(*recordingChannel).sentSnapshot()
			switch {
			case tc.wantSilent:
				if outcome != "ignored_duplicate_reply" || len(sent) != 1 {
					t.Fatalf("outcome=%s sent=%#v", outcome, sent)
				}
			case tc.action == "rewrite":
				if len(sent) != 2 || sent[1].Text != "新增信息" {
					t.Fatalf("sent=%#v", sent)
				}
				if len(p.audits) == 0 || !strings.Contains(p.audits[len(p.audits)-1], "新增信息") {
					t.Fatal("rewritten text bypassed audit")
				}
			default:
				// 判定仍然是 drop，但直接触发不许静默丢弃，原候选照常发出去。
				if outcome != "replied" || len(sent) != 2 || sent[1].Text != "原有说明和新增信息" {
					t.Fatalf("outcome=%s sent=%#v", outcome, sent)
				}
			}
			// 无论哪条路径，判定都真的跑过一次，而且上一轮发出去的内容进了去重依据。
			if len(p.requests) != 1 {
				t.Fatalf("dedup calls=%d", len(p.requests))
			}
			var payload struct {
				Recent []semanticSentReply `json:"recent_sent"`
			}
			if err := json.Unmarshal([]byte(p.requests[0].Messages[1].Content), &payload); err != nil || len(payload.Recent) != 1 {
				t.Fatalf("payload=%#v err=%v", payload, err)
			}
		})
	}
}

func TestSemanticReplyCancellationExpiryAndProtectedContent(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"rewrite","confidence":0.99,"content":"删掉代码"}`}
	r := topicTestRuntime(p)
	event := directedGroupMessage("m", "u", "问题")
	g, release, _ := r.lockSemanticReply(context.Background(), event)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.lockSemanticReply(ctx, event); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	g.sent = []semanticSentReply{{Reply: "过期答复", SentAt: time.Now().Add(-3 * time.Minute)}}
	if _, err := r.deduplicateReply(context.Background(), event, "问题", "正文", BotConfig{}, g, true); err != nil || len(p.requests) != 0 {
		t.Fatal("expired history used")
	}
	g.remember("问题", "已发送")
	original := "正文\n```go\nprintln(1)\n```"
	if got, err := r.deduplicateReply(context.Background(), event, "问题", original, BotConfig{}, g, true); got != original || err != nil {
		t.Fatalf("protected content changed: %q %v", got, err)
	}
	release()
}

type semanticFailChannel struct{ recordingChannel }

func (c *semanticFailChannel) SendWithResult(context.Context, OutgoingMessage) (map[string]any, error) {
	return nil, errors.New("test transport failure")
}

func TestSemanticReplyFailedSendDoesNotBecomeEvidence(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"drop","confidence":0.99}`}
	r := NewRuntime(BotConfig{BotAccount: "42", ErrorNotifyEnabled: boolPointer(false), SendRetryAttempts: 1}, &semanticFailChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	event := directedGroupMessage("failed", "u", "问题")
	_, _ = r.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
	gate, release, err := r.lockSemanticReply(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(gate.sent) != 0 {
		t.Fatal("failed send entered success evidence")
	}
}

type semanticBlockingChannel struct {
	recordingChannel
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *semanticBlockingChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	c.once.Do(func() {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestSemanticReplyConcurrentGenerationsDeliverOnce(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"drop","confidence":0.99}`}
	c := &semanticBlockingChannel{started: make(chan struct{}), release: make(chan struct{})}
	r := NewRuntime(BotConfig{BotAccount: "42"}, c, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan string, 2)
	run := func(id string) {
		// 丢弃只在主动接话这条路径上允许，这条用例钉的就是那里。
		event := proactiveGroupMessage(id, "u", "问题")
		outcome, err := r.replyAndRecord(ctx, event, event.RawMessage, "replied")
		if err != nil {
			done <- err.Error()
		} else {
			done <- outcome
		}
	}
	go run("first")
	select {
	case <-c.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go run("second")
	close(c.release)
	results := map[string]int{}
	for i := 0; i < 2; i++ {
		select {
		case result := <-done:
			results[result]++
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if results["replied"] != 1 || results["ignored_duplicate_reply"] != 1 || len(c.sentSnapshot()) != 1 {
		t.Fatalf("outcomes=%v sent=%#v", results, c.sentSnapshot())
	}
}

func TestSemanticReplyDropCannotConsumeNewSupplement(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"drop","confidence":0.99}`}
	r := topicTestRuntime(p)
	event := directedGroupMessage("root", "u", "问题")
	ctx, finish := r.beginDirectReply(withReplyTriggerGate(context.Background()), event)
	defer finish()
	ctx = r.directReplyAttemptContext(ctx)
	p.onJudge = func() {
		r.replyInterruptMu.Lock()
		r.activeDirectReplies[directReplyMergeKey(event)].generation++
		r.replyInterruptMu.Unlock()
	}
	g, release, _ := r.lockSemanticReply(ctx, event)
	defer release()
	g.remember("之前的问题", "已发送")
	if _, err := r.deduplicateReply(ctx, event, "问题", "候选", BotConfig{}, g, true); !errors.Is(err, errDirectReplySupplemented) {
		t.Fatalf("lost new supplement: %v", err)
	}
}
