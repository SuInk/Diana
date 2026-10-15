package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Test-only prototype: atomically append without a topic-model call. Production
// routing is unchanged until this experiment's boundaries have been verified.
func appendUnsentTurnExperiment(r *Runtime, event MessageEvent) bool {
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	a := r.activeDirectReplies[directReplyMergeKey(event)]
	if a == nil || !a.accepting || a.root.MessageID == event.MessageID {
		return false
	}
	for _, s := range a.supplements {
		if s.Event.MessageID == event.MessageID {
			return true
		}
	}
	a.generation++
	a.supplements = append(a.supplements, proactiveReplyCandidate{Event: event, Text: event.RawMessage, QueuedAt: time.Now(), Generation: a.generation})
	return true
}

func TestDirectAppendExperimentBoundaries(t *testing.T) {
	r := topicTestRuntime(nil)
	root := directedGroupMessage("root", "u", "名字")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	follow := directedGroupMessage("follow", "u", "实际需求")
	other := follow
	other.UserID = "other"
	if appendUnsentTurnExperiment(r, other) {
		t.Fatal("cross-user merge")
	}
	other = follow
	other.GroupID = "other"
	if appendUnsentTurnExperiment(r, other) {
		t.Fatal("cross-session merge")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); appendUnsentTurnExperiment(r, follow) }()
	}
	wg.Wait()
	if len(r.directReplySupplements(ctx)) != 1 || !r.directReplyHasNewSupplements(ctx) {
		t.Fatal("lost or duplicated supplement")
	}
	finish()
	if appendUnsentTurnExperiment(r, follow) {
		t.Fatal("merged after completion")
	}
}

type appendExperimentProvider struct {
	LLMProvider
	mu         sync.Mutex
	calls      int
	topicCalls int
	sawFollow  bool
	follow     string
}

func (p *appendExperimentProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.calls++
	for _, m := range req.Messages {
		if strings.Contains(m.Content, directReplyTopicPrompt) {
			p.topicCalls++
		}
		if strings.Contains(m.Content, p.follow) {
			p.sawFollow = true
		}
	}
	p.mu.Unlock()
	return p.LLMProvider.Generate(ctx, req)
}

func TestLiveDirectAppendExperiment(t *testing.T) {
	client := liveLLMClient(t)
	cases := []struct {
		name, root, follow string
		gap                time.Duration
	}{{"screenshot", "美海宝宝", "给我法", time.Second}}
	if path := os.Getenv("DIANA_BURST_REPLAY_ROWS"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			Root, Follow MessageEvent
			Gap          float64
			Bucket       string
		}
		if err = json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Bucket == "allowed_separate" && row.Gap > 9 && row.Gap < 10 {
				cases = append(cases, struct {
					name, root, follow string
					gap                time.Duration
				}{"production_docker", row.Root.RawMessage, row.Follow.RawMessage, time.Duration(row.Gap * float64(time.Second))})
				break
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &appendExperimentProvider{LLMProvider: client, follow: tc.follow}
			r := topicTestRuntime(p)
			cfg := r.ProfileConfig("")
			cfg.GroupTriggers = []string{"美海"}
			if err := r.ApplyProfiles(context.Background(), NewProfileSet(cfg), nil); err != nil {
				t.Fatal(err)
			}
			root := directedGroupMessage("root", "replay-user", tc.root)
			follow := directedGroupMessage("follow", "replay-user", tc.follow)
			r.remember(root)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := r.replyAndRecord(ctx, root, root.RawMessage, "replied"); done <- err }()
			timer := time.NewTimer(tc.gap)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			before := len(r.channel.(*recordingChannel).sentSnapshot())
			appendStart := time.Now()
			accepted := appendUnsentTurnExperiment(r, follow)
			appendElapsed := time.Since(appendStart)
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
			calls, topics, saw := p.calls, p.topicCalls, p.sawFollow
			p.mu.Unlock()
			t.Logf("gap=%s accepted=%v append_us=%d sent_before=%d sent_final=%d calls=%d topic_calls=%d follow_in_prompt=%v elapsed=%s", tc.gap, accepted, appendElapsed.Microseconds(), before, len(sent), calls, topics, saw, time.Since(start))
			for i, msg := range sent {
				t.Logf("sent[%d]=%q", i, msg.Text)
			}
			if topics != 0 {
				t.Error("unexpected relation model call")
			}
			if accepted && (!saw || len(sent) != 1) {
				t.Error("accepted supplement missing or multiple sends")
			}
			if appendUnsentTurnExperiment(r, directedGroupMessage("later", "replay-user", "下一轮")) {
				t.Error("accepted after turn finished")
			}
		})
	}
}
