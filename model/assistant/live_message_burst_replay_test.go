package assistant

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Replay the screenshot through the real preprocessing, routing, generation,
// and audit paths. Only transport is recorded locally. This is a diagnostic
// replay: log outcomes rather than assuming the existing implementation merges.
func TestLiveMessageBurstScreenshotReplay(t *testing.T) {
	client := liveLLMClient(t)
	gap := time.Second
	if raw := os.Getenv("DIANA_REPLAY_GAP"); raw != "" {
		var err error
		gap, err = time.ParseDuration(raw)
		if err != nil || gap < 0 || gap > time.Minute {
			t.Fatalf("invalid replay gap %q", raw)
		}
	}
	h := newLiveDampingHarness(t, client)
	cfg := h.runtime.ProfileConfig("")
	cfg.GroupTriggers = []string{"美海", "小美海"}
	if err := h.runtime.ApplyProfiles(context.Background(), NewProfileSet(cfg), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	start := time.Now()
	h.send(ctx, "replay-user", "Winter", "burst-call", "美海宝宝")
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Logf("gap=%s outbound_before_followup=%d", gap, len(h.channel.sentSnapshot()))
	h.send(ctx, "replay-user", "Winter", "burst-request", "给我法")
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replied, damped, suppressed, outcomes := h.summary()
	t.Logf("gap=%s elapsed=%s replied_turns=%d damped=%d suppressed=%d outcomes=%v", gap, time.Since(start).Round(time.Millisecond), replied, damped, suppressed, outcomes)
	for _, outcome := range outcomes {
		if strings.Contains(outcome, "=error:") {
			t.Error(outcome)
		}
	}
	sent := h.channel.sentSnapshot()
	t.Logf("outbound_messages=%d", len(sent))
	for i, message := range sent {
		t.Logf("sent[%d]=%q", i, message.Text)
	}
}
