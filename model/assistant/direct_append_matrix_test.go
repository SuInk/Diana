package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestDirectAppendExperimentBurstAndPayloads(t *testing.T) {
	r := topicTestRuntime(nil)
	root := directedGroupMessage("root", "u", "原问题")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := directedGroupMessage(fmt.Sprint(i), "u", fmt.Sprintf("补充%d", i))
			if !appendUnsentTurnExperiment(r, e) {
				t.Error("lost message")
			}
		}(i)
	}
	wg.Wait()
	if got := len(r.directReplySupplements(ctx)); got != 100 {
		t.Fatalf("supplements=%d", got)
	}
	quoted := quotedPowerEvent("quoted", "80")
	quoted.UserID = root.UserID
	quoted.GroupID = root.GroupID
	quoted.Segments = append(quoted.Segments, MessageSegment{Type: "image", Data: map[string]string{"file": "test-image"}})
	if !appendUnsentTurnExperiment(r, quoted) {
		t.Fatal("quote rejected")
	}
	got := r.directReplySupplements(ctx)[100].Event
	if got.Quoted == nil || got.Quoted.RawMessage != "按80W算吧" || !hasImageSegment(got.Segments) {
		t.Fatal("quote/media lost")
	}
	r.sealDirectReply(ctx)
	if appendUnsentTurnExperiment(r, directedGroupMessage("sealed", "u", "新消息")) {
		t.Fatal("accepted sealed turn")
	}
}

// The first real model request is held at its return boundary, making arrival
// during generation deterministic. This is controlled scheduling, not a replay
// of production latency. No model output is mocked.
type matrixAppendProvider struct {
	LLMProvider
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   int
	topics  int
	prompts []string
}

func (p *matrixAppendProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	for _, m := range req.Messages {
		p.prompts = append(p.prompts, m.Content)
		if strings.Contains(m.Content, directReplyTopicPrompt) {
			p.topics++
		}
	}
	p.mu.Unlock()
	if first {
		close(p.started)
	}
	resp, err := p.LLMProvider.Generate(ctx, req)
	if first {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return resp, err
}
func TestLiveDirectAppendMatrix(t *testing.T) {
	client := liveLLMClient(t)
	cases := []struct {
		name, root  string
		supplements []string
		quote       bool
	}{
		{"three_supplements", "帮我写一句活动通知", []string{"活动是周六读书会", "下午三点开始", "地点是图书馆二楼，通知里都要写上"}, false},
		{"correction", "45W设备工作一小时耗电多少度", []string{"改成80W", "运行两小时，按最新条件算"}, false},
		{"topic_switch", "解释一下标准输出", []string{"先不问这个了", "告诉我17乘以23是多少"}, false},
		{"quoted_correction", "45W设备工作一小时耗电多少度", []string{"按引用的新功率重新算"}, true},
		{"production_docker", "你如果给新机器装docker 你用我那一套就行了", []string{"就把那个service.sh拷上去"}, false},
		{"production_fragment", "如果这个时候有v skills.", []string{"就好了"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &matrixAppendProvider{LLMProvider: client, started: make(chan struct{}), release: make(chan struct{})}
			r := topicTestRuntime(p)
			root := directedGroupMessage("root", "u", tc.root)
			r.remember(root)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := r.replyAndRecord(ctx, root, root.RawMessage, "replied"); done <- err }()
			select {
			case <-p.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for i, text := range tc.supplements {
				e := directedGroupMessage(fmt.Sprintf("follow-%d", i), "u", text)
				if tc.quote {
					e.Quoted = &QuotedMessage{MessageID: "quote", UserID: "other", RawMessage: "按80W算吧"}
				}
				r.remember(e)
				if !appendUnsentTurnExperiment(r, e) {
					t.Error("supplement rejected")
				}
			}
			close(p.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			sent := r.channel.(*recordingChannel).sentSnapshot()
			p.mu.Lock()
			all := strings.Join(p.prompts, "\n")
			topics, calls := p.topics, p.calls
			p.mu.Unlock()
			for _, text := range tc.supplements {
				if !strings.Contains(all, text) {
					t.Errorf("missing supplement %q", text)
				}
			}
			if tc.quote && !strings.Contains(all, "按80W算吧") {
				t.Error("quoted correction absent")
			}
			if topics != 0 {
				t.Error("topic model called")
			}
			if len(sent) != 1 {
				t.Errorf("sends=%d want 1", len(sent))
			}
			t.Logf("supplements=%d calls=%d topics=%d sends=%d elapsed=%s", len(tc.supplements), calls, topics, len(sent), time.Since(start))
			for i, msg := range sent {
				t.Logf("reply[%d]=%q", i, msg.Text)
			}
			// Actually generate a new turn after completion, rather than only asserting
			// the old turn rejects further input.
			if tc.name == "correction" {
				next := directedGroupMessage("next", "u", "现在换成100W运行三小时，耗电多少度")
				if appendUnsentTurnExperiment(r, next) {
					t.Fatal("accepted into completed turn")
				}
				r.remember(next)
				outcome, err := r.replyAndRecord(ctx, next, next.RawMessage, "replied")
				if err != nil || outcome != "replied" {
					t.Fatalf("next turn=%s err=%v", outcome, err)
				}
				after := r.channel.(*recordingChannel).sentSnapshot()
				if len(after) <= len(sent) {
					t.Fatalf("new turn sent no reply: messages=%d", len(after))
				}
				var newReply strings.Builder
				for i, msg := range after[len(sent):] {
					t.Logf("next_turn_fragment[%d]=%q", i, msg.Text)
					newReply.WriteString(msg.Text)
				}
				if !strings.Contains(newReply.String(), "0.3") {
					t.Errorf("new turn did not answer updated calculation: %q", newReply.String())
				}
			}
		})
	}
}
