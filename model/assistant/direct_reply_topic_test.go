package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type topicTestProvider struct {
	base    *directReplyMergeProvider
	result  string
	err     error
	onTopic func(llm.GenerateRequest)
}

func (p *topicTestProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, directReplyTopicPrompt) {
			if p.onTopic != nil {
				p.onTopic(req)
			}
			return &llm.GenerateResponse{Text: p.result}, p.err
		}
	}
	return p.base.Generate(ctx, req)
}

func topicTestRuntime(p LLMProvider) *Runtime {
	return NewRuntime(BotConfig{BotAccount: "42"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
}

func TestDirectReplyTopicFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		err          error
		merge        bool
	}{
		{"supplement", `{"relation":"supplement","confidence":0.99}`, nil, true},
		{"separate", `{"relation":"separate","confidence":0.99}`, nil, false},
		{"unknown", `{"relation":"unknown","confidence":0.99}`, nil, false},
		{"low confidence", `{"relation":"supplement","confidence":0.89}`, nil, false},
		{"missing confidence", `{"relation":"supplement"}`, nil, false},
		{"invalid confidence", `{"relation":"supplement","confidence":2}`, nil, false},
		{"malformed", `not json`, nil, false},
		{"failure", "", errors.New("offline"), false},
		{"timeout", "", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &topicTestProvider{result: tc.result, err: tc.err}
			r := topicTestRuntime(p)
			root := directedGroupMessage("root", "user", "解释 stdout")
			ctx, finish := r.beginDirectReply(context.Background(), root)
			defer finish()
			follow := directedGroupMessage("follow", "user", "再举个 stdout 的例子")
			_, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
			if merged != tc.merge || (len(r.directReplySupplements(ctx)) == 1) != tc.merge {
				t.Fatalf("merged=%v, want %v", merged, tc.merge)
			}
		})
	}
}

func TestDirectReplySeparateTopicKeepsOriginalAnswer(t *testing.T) {
	base := &directReplyMergeProvider{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	p := &topicTestProvider{base: base, result: `{"relation":"separate","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "你搜索下 x.com")
	root.replyHistoryLoaded = true
	root.replyHistory = []MessageEvent{directedGroupMessage("earlier", "user", "OpenAI 产品经理 Tibo 怎么还在发言")}
	p.onTopic = func(req llm.GenerateRequest) {
		if !strings.Contains(req.Messages[1].Content, "Tibo") {
			t.Error("topic classifier lost original question context")
		}
	}
	r.noteDirectedInbound(root)
	r.remember(root)
	done := make(chan error, 1)
	go func() {
		_, err := r.replyAndRecord(withOutboundTurn(context.Background(), "root-turn"), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-base.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first generation did not start")
	}
	follow := directedGroupMessage("follow", "user", "是 x 的产品经理换了，然后收益模式改了")
	r.noteDirectedInbound(follow)
	_, _, handled, outcome := r.prepareMessageEvent(context.Background(), follow)
	close(base.releaseFirst)
	if !handled || outcome == "merged_into_reply" {
		t.Errorf("independent question consumed: handled=%v outcome=%s", handled, outcome)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original reply did not finish")
	}
	if sent := r.channel.(*recordingChannel).sentSnapshot(); len(sent) != 1 || sent[0].Text != "只回答第一条" {
		t.Fatalf("original answer lost: %#v", sent)
	}
	if _, err := r.replyAndRecord(withOutboundTurn(context.Background(), "follow-turn"), follow, follow.RawMessage, "replied"); err != nil {
		t.Fatal(err)
	}
	if sent := r.channel.(*recordingChannel).sentSnapshot(); len(sent) != 2 {
		t.Fatalf("independent question did not get its own answer: %#v", sent)
	}
}

func TestDirectReplyTopicRechecksAfterClassification(t *testing.T) {
	for _, mode := range []string{"finished", "sealed", "replaced", "changed"} {
		t.Run(mode, func(t *testing.T) {
			p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
			r := topicTestRuntime(p)
			root := directedGroupMessage("root", "user", "stdout")
			ctx, finish := r.beginDirectReply(context.Background(), root)
			defer finish()
			p.onTopic = func(llm.GenerateRequest) {
				switch mode {
				case "finished":
					finish()
				case "sealed":
					r.sealDirectReply(ctx)
				case "replaced":
					_, end := r.beginDirectReply(context.Background(), root)
					defer end()
				case "changed":
					r.replyInterruptMu.Lock()
					r.activeDirectReplies[directReplyMergeKey(root)].generation++
					r.replyInterruptMu.Unlock()
				}
			}
			follow := directedGroupMessage("follow", "user", "example")
			if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); merged {
				t.Fatal("stale classification consumed follow-up")
			}
		})
	}
}

func TestDirectReplyConcurrentTurnsKeepSupplements(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	follow := directedGroupMessage("follow", "user", "stdout example")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	next, end := r.beginDirectReply(context.Background(), directedGroupMessage("next", "user", "another topic"))
	defer end()
	if len(r.directReplySupplements(ctx)) != 1 || len(r.directReplySupplements(next)) != 0 {
		t.Fatal("overlapping turn lost or inherited another turn's supplements")
	}
	if !r.directReplyHasNewSupplements(ctx) {
		t.Fatal("old turn lost generation state")
	}
	attempt := r.directReplyAttemptContext(ctx)
	if r.directReplyHasNewSupplements(attempt) {
		t.Fatal("generation not acknowledged")
	}
	r.replyInterruptMu.Lock()
	accepting := attempt.Value(directReplyRunContextKey{}).(directReplyRunContext).active.accepting
	r.replyInterruptMu.Unlock()
	if accepting {
		t.Fatal("send gate did not seal turn")
	}
}
