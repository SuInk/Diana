package assistant

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func summaryTestEvent() MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10002", MessageID: "current", RawMessage: "Diana 帮我看看", Time: 100}
}

func setPromptSummary(r *Runtime, event MessageEvent, summary string) {
	r.mu.Lock()
	r.contextSummaries[sessionKey(event)] = summary
	r.mu.Unlock()
}

func TestGroupPromptSummaryUsesLatestSourceAndBudget(t *testing.T) {
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(store)
	event := summaryTestEvent()
	setPromptSummary(r, event, "旧摘要")
	if got, _ := r.promptContextSummary(event, "", 1000); got != "旧摘要" {
		t.Fatal(got)
	}
	setPromptSummary(r, event, "最新摘要 [图片]")
	if got, _ := r.promptContextSummary(event, "", 1000); got != "最新摘要" {
		t.Fatalf("latest source lost to checkpoint: %q", got)
	}
	source := strings.Repeat("一条很长的旧记录，需要给最新记录让位。\n", 200) + "最后一条记录。"
	setPromptSummary(r, event, source)
	want, wantReduced := r.fitOlderSummaryToBudget(source, 1000)
	got, reduced := r.promptContextSummary(event, "", 1000)
	if got != want || reduced != wantReduced || !reduced {
		t.Fatalf("budget not applied: reduced=%v wantReduced=%v", reduced, wantReduced)
	}
	restarted := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	restarted.SetMessageHistoryStore(store)
	want, wantReduced = r.fitOlderSummaryToBudget(got, minimumContextSummaryTokens/2)
	got, reduced = restarted.promptContextSummary(event, "", minimumContextSummaryTokens/2)
	if got != want || reduced != wantReduced {
		t.Fatal("restored summary bypassed fitting")
	}
	setPromptSummary(restarted, event, "")
	if got, _ := restarted.promptContextSummary(event, "", 1000); got != "" {
		t.Fatalf("explicitly empty source restored old checkpoint: %q", got)
	}
}

func TestGroupPromptSummaryTakeoverClearsCheckpoint(t *testing.T) {
	for _, takeover := range []string{"thread", "structured"} {
		t.Run(takeover, func(t *testing.T) {
			store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
			event := summaryTestEvent()
			r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			r.SetMessageHistoryStore(store)
			setPromptSummary(r, event, "不能复活的旧摘要")
			r.promptContextSummary(event, "", 1000)
			// Restart leaves only the durable fallback, which must also be suppressed.
			r = NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			r.SetMessageHistoryStore(store)
			thread := ""
			if takeover == "thread" {
				thread = "会话线程已接管"
			} else {
				r.SetStructuredMemoryStore(&testStructuredMemoryStore{})
			}
			if got, _ := r.promptContextSummary(event, thread, 1000); got != "" {
				t.Fatalf("takeover restored checkpoint: %q", got)
			}
			r = NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			r.SetMessageHistoryStore(store)
			if got, _ := r.promptContextSummary(event, "", 1000); got != "" {
				t.Fatalf("takeover failed to clear durable checkpoint: %q", got)
			}
		})
	}
}

// The routing call happens after the early availability snapshot. Changing the
// source here verifies the complete reply request does not publish that snapshot.
type summaryRequestProvider struct {
	requests []llm.GenerateRequest
	onRoute  func()
}

func (p *summaryRequestProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "older_summary_available") {
			if p.onRoute != nil {
				p.onRoute()
			}
			return &llm.GenerateResponse{Text: `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":true}`}, nil
		}
	}
	return &llm.GenerateResponse{Text: "收到。"}, nil
}

func TestGroupPromptSummaryFullRequestStablePrefix(t *testing.T) {
	provider := &summaryRequestProvider{}
	r := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, LLMIdentityMaskingEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := summaryTestEvent()
	old := event
	old.MessageID, old.RawMessage, old.Time = "old", "保持稳定的群聊历史", 50
	r.remember(old)
	var prefixes [][]llm.Message
	for _, latest := range []string{"第一轮最新摘要", "第二轮最新摘要"} {
		// Hold public history fixed to isolate summary churn from outgoing replies.
		r.mu.Lock()
		r.history[sessionKey(event)] = []MessageEvent{old}
		r.groupPromptHistory[groupPromptSessionKey(event)] = groupPromptHistoryBuffer{Session: sessionKey(event), Events: []MessageEvent{old}}
		r.mu.Unlock()
		setPromptSummary(r, event, "意图路由前的过期摘要")
		routed := false
		provider.onRoute = func() { routed = true; setPromptSummary(r, event, latest) }
		provider.requests = nil
		if _, err := r.replyTo(context.Background(), event, event.RawMessage); err != nil {
			t.Fatal(err)
		}
		if !routed {
			t.Fatal("fixture did not exercise routing-time summary update")
		}
		var request *llm.GenerateRequest
		for i := range provider.requests {
			for _, msg := range provider.requests[i].Messages {
				if strings.Contains(msg.Content, "【当前需要回复的消息】") {
					request = &provider.requests[i]
				}
			}
		}
		if request == nil {
			t.Fatal("missing full reply request")
		}
		boundary, summaryIndex, summaries := -1, -1, 0
		for i, msg := range request.Messages {
			if msg.CacheBreakpoint {
				boundary = i
			}
			if strings.Contains(msg.Content, "意图路由前的过期摘要") {
				t.Fatal("early routing summary leaked into reply")
			}
			if strings.HasPrefix(msg.Content, "【较早上下文压缩摘要") {
				summaryIndex, summaries = i, summaries+1
				if !strings.Contains(msg.Content, latest) || msg.Priority != llm.MessagePrioritySummary || !msg.AtomicText {
					t.Fatalf("summary source or metadata changed: %+v", msg)
				}
			}
		}
		if boundary < 1 || summaryIndex <= boundary || summaries != 1 {
			t.Fatalf("boundary=%d summaryIndex=%d summaries=%d", boundary, summaryIndex, summaries)
		}
		prefixes = append(prefixes, append([]llm.Message(nil), request.Messages[:boundary+1]...))
	}
	if !reflect.DeepEqual(prefixes[0], prefixes[1]) {
		t.Fatal("summary update changed complete stable request prefix")
	}
}

type summaryCASStore struct {
	*promptSessionTestStore
	generation int64
	beforeSave func(GroupPromptSession)
	resetDone  chan struct{}
}

func (s *summaryCASStore) LoadGroupPromptSession(ctx context.Context, key, session string) (GroupPromptSession, error) {
	state, err := s.promptSessionTestStore.LoadGroupPromptSession(ctx, key, session)
	s.mu.Lock()
	state.Generation = s.generation
	s.mu.Unlock()
	return state, err
}

func (s *summaryCASStore) SaveGroupPromptSession(_ context.Context, key, _ string, state GroupPromptSession) (bool, error) {
	if s.beforeSave != nil {
		s.beforeSave(state)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.Generation != s.generation {
		return false, nil
	}
	s.states[key] = state
	return true, nil
}

func (s *summaryCASStore) ResetContextHistory(_ context.Context, _ string, _ time.Time) error {
	s.mu.Lock()
	s.generation++
	s.states = map[string]GroupPromptSession{}
	s.mu.Unlock()
	if s.resetDone != nil {
		close(s.resetDone)
	}
	return nil
}

func (s *summaryCASStore) ListContextMessageEvents(ctx context.Context, session string, limit int) ([]MessageEvent, error) {
	return s.ListRecentMessageEvents(ctx, session, limit)
}

func awaitSummarySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("summary operation blocked (possible lock inversion or store I/O under runtime lock)")
	}
}

func TestGroupPromptSummaryConcurrentPublicationAndReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "newer_source"
		if reset {
			name = "reset"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			defer unblock.Do(func() { close(release) })
			store := &summaryCASStore{promptSessionTestStore: &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}, resetDone: make(chan struct{})}
			store.beforeSave = func(state GroupPromptSession) {
				if state.Checkpoint == "旧摘要" {
					close(entered)
					<-release
				}
			}
			r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			r.SetMessageHistoryStore(store)
			event := summaryTestEvent()
			setPromptSummary(r, event, "旧摘要")
			session := r.groupPromptSession(event)
			oldDone := make(chan struct{})
			var oldSummary string
			go func() { oldSummary, _ = r.promptContextSummary(event, "", 1000); close(oldDone) }()
			awaitSummarySignal(t, entered)
			newDone := make(chan struct{})
			if reset {
				go func() {
					if err := r.clearSessionHistory(event); err != nil {
						t.Error(err)
					}
					close(newDone)
				}()
				awaitSummarySignal(t, store.resetDone)
			} else {
				updated := make(chan struct{})
				go func() { setPromptSummary(r, event, "新摘要"); close(updated) }()
				awaitSummarySignal(t, updated)
				go func() { r.promptContextSummary(event, "", 1000); close(newDone) }()
			}
			unblock.Do(func() { close(release) })
			awaitSummarySignal(t, oldDone)
			awaitSummarySignal(t, newDone)
			want := "新摘要"
			if reset {
				want = ""
				if oldSummary != "" || !session.invalidated {
					t.Fatal("stale generation was accepted after reset")
				}
			}
			if got, _ := r.promptContextSummary(event, "", 1000); got != want {
				t.Fatalf("summary after concurrent work = %q, want %q", got, want)
			}
			restarted := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			restarted.SetMessageHistoryStore(store)
			if got, _ := restarted.promptContextSummary(event, "", 1000); got != want {
				t.Fatalf("durable summary after concurrent work = %q, want %q", got, want)
			}
		})
	}
}
