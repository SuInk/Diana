// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"os"
	"testing"
	"time"
)

// waitFixture is a helper whose screenshot reads <helper>.scene and whose type
// command records the typed text into <helper>.typed, so a test can tell
// whether input was actually sent.
func waitFixture(t *testing.T, wait time.Duration) (*ProcessAdapter, string, string) {
	t.Helper()
	a := processFixture(t, `case "$1" in
 screenshot) printf '\211PNG\r\n\032\n'; cat "${0}.scene" ;;
 type) cat > "${0}.typed"; printf '{"ok":true}' ;;
 esac`)
	a.SetConfirmationWait(wait)
	scene, typed := a.path+".scene", a.path+".typed"
	if err := os.WriteFile(scene, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	return a, scene, typed
}

type actionOutcome struct {
	result ActionResult
	err    error
}

func startTyping(t *testing.T, ctx context.Context, a *ProcessAdapter, text string) (Command, <-chan actionOutcome) {
	t.Helper()
	shot, err := a.screenshot(context.Background(), "42", "app")
	if err != nil {
		t.Fatal(err)
	}
	c := Command{Op: OpWindowType, WindowID: "42", ExpectedBundleID: "app", Text: text, Observation: shot.Observation}
	done := make(chan actionOutcome, 1)
	go func() {
		r, err := a.TypeText(ctx, c)
		done <- actionOutcome{r, err}
	}()
	return c, done
}

func waitPending(t *testing.T, a *ProcessAdapter) ActionApproval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, _ := a.PendingActions(context.Background())
		if len(pending) == 1 {
			return pending[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending confirmation appeared")
	return ActionApproval{}
}

func outcome(t *testing.T, done <-chan actionOutcome) actionOutcome {
	t.Helper()
	select {
	case o := <-done:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("action did not return")
		return actionOutcome{}
	}
}

func notTyped(t *testing.T, typed string) {
	t.Helper()
	if _, err := os.Stat(typed); err == nil {
		t.Fatal("input was sent without a valid confirmation")
	}
}

func TestActionWaitsForOwnerThenRunsInSameCall(t *testing.T) {
	a, _, typed := waitFixture(t, time.Minute)
	notified := make(chan ConfirmationRequest, 1)
	ctx := WithConfirmationNotifier(context.Background(), func(_ context.Context, req ConfirmationRequest) { notified <- req })
	c, done := startTyping(t, ctx, a, "hello")
	pending := waitPending(t, a)
	select {
	case req := <-notified:
		if req.ID != pending.ID || req.Command.Text != "hello" {
			t.Fatalf("notice does not describe the pending action: %+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner was not notified")
	}
	select {
	case o := <-done:
		t.Fatalf("returned before the owner confirmed: %+v", o)
	case <-time.After(50 * time.Millisecond):
	}
	notTyped(t, typed)
	if err := a.ConfirmAction(context.Background(), actionKey(c)); err != nil {
		t.Fatal(err)
	}
	o := outcome(t, done)
	if o.err != nil || !o.result.OK || o.result.Evidence == nil {
		t.Fatalf("confirmed action did not run: %+v %v", o.result, o.err)
	}
	if got, _ := os.ReadFile(typed); string(got) != "hello" {
		t.Fatalf("typed %q", got)
	}
	if pending, _ := a.PendingActions(context.Background()); len(pending) != 0 {
		t.Fatalf("grant not consumed: %+v", pending)
	}
}

// The owner may take longer than the two-minute observation TTL to decide;
// the observation bound to a pending request stays valid until it expires.
func TestPendingConfirmationKeepsObservationAlive(t *testing.T) {
	a, _, typed := waitFixture(t, time.Minute)
	c, done := startTyping(t, context.Background(), a, "late")
	waitPending(t, a)
	if err := a.lockSequence(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs := a.observations[c.Observation]
	obs.at = time.Now().Add(-3 * observationTTL)
	a.observations[c.Observation] = obs
	a.unlockSequence()
	// Another window's screenshot prunes expired observations; this one is pinned.
	if _, err := a.screenshot(context.Background(), "43", "app"); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmAction(context.Background(), actionKey(c)); err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, done); o.err != nil {
		t.Fatal(o.err)
	}
	if got, _ := os.ReadFile(typed); string(got) != "late" {
		t.Fatalf("typed %q", got)
	}
}

func TestActionWaitTimesOutWithoutSending(t *testing.T) {
	a, _, typed := waitFixture(t, 80*time.Millisecond)
	_, done := startTyping(t, context.Background(), a, "never")
	o := outcome(t, done)
	if ErrorCode(o.err) != CodeConfirmationTimeout {
		t.Fatalf("want confirmation_timeout, got %v", o.err)
	}
	notTyped(t, typed)
	if pending, _ := a.PendingActions(context.Background()); len(pending) != 0 {
		t.Fatalf("expired request still listed: %+v", pending)
	}
}

func TestConfirmedActionRechecksSceneShownToOwner(t *testing.T) {
	a, scene, typed := waitFixture(t, time.Minute)
	c, done := startTyping(t, context.Background(), a, "hello")
	waitPending(t, a)
	if err := os.WriteFile(scene, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmAction(context.Background(), actionKey(c)); err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, done); ErrorCode(o.err) != CodeStaleObservation {
		t.Fatalf("want stale_observation, got %v", o.err)
	}
	notTyped(t, typed)
}

func TestCancelledWaitDropsPendingGrant(t *testing.T) {
	a, _, typed := waitFixture(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	_, done := startTyping(t, ctx, a, "hello")
	waitPending(t, a)
	cancel()
	if o := outcome(t, done); o.err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	notTyped(t, typed)
	if pending, _ := a.PendingActions(context.Background()); len(pending) != 0 {
		t.Fatalf("abandoned request still confirmable: %+v", pending)
	}
}

// Takeover and reconnect invalidate state; a waiting call must stop at once
// instead of sitting out the full confirmation window.
func TestInvalidateEndsWaitingAction(t *testing.T) {
	a, _, typed := waitFixture(t, time.Minute)
	_, done := startTyping(t, context.Background(), a, "hello")
	waitPending(t, a)
	a.Invalidate(context.Background())
	if o := outcome(t, done); ErrorCode(o.err) != CodeStaleObservation {
		t.Fatalf("want stale_observation, got %v", o.err)
	}
	notTyped(t, typed)
}

// Through the Hub the reply deadline covers the confirmation wait, the notice
// names the app from the trusted window list, and takeover ends the wait.
func TestHubDispatchWaitsForConfirmationAndNamesApp(t *testing.T) {
	a := processFixture(t, `
case "$1" in
screenshot) printf '\211PNG\r\n\032\nfixture' ;;
list) printf '{"windows":[{"id":"42","app_name":"Fixture","bundle_id":"com.example.fixture","active":true}]}' ;;
type) cat > "${0}.typed"; printf '{"ok":true}' ;;
esac`)
	a.SetConfirmationWait(time.Minute)
	ctx := context.Background()
	r := NewRegistry(ctx, nil)
	policy := writePolicy()
	policy.CommandTimeoutMS = 100
	_, _ = r.SetPolicy(ctx, policy)
	h := NewHub(r)
	defer h.CloseAll()
	c, _, err := h.AttachLocal(ctx, a, Hello{}, TokenInfo{})
	if err != nil {
		t.Fatal(err)
	}
	PublishWindows(c, nil)
	dispatch := func(text string) (<-chan actionOutcome, <-chan ConfirmationRequest) {
		shot, err := a.screenshot(ctx, "42", "com.example.fixture")
		if err != nil {
			t.Fatal(err)
		}
		notified := make(chan ConfirmationRequest, 1)
		notifyCtx := WithConfirmationNotifier(ctx, func(_ context.Context, req ConfirmationRequest) { notified <- req })
		done := make(chan actionOutcome, 1)
		go func() {
			_, err := h.Dispatch(notifyCtx, Command{Op: OpWindowType, WindowID: "42", Text: text, Observation: shot.Observation})
			done <- actionOutcome{err: err}
		}()
		return done, notified
	}

	done, notified := dispatch("hello")
	pending := waitPending(t, a)
	if req := <-notified; req.AppName != "Fixture" || req.ID != pending.ID {
		t.Fatalf("notice=%+v", req)
	}
	// Longer than CommandTimeoutMS: the wait must not be cut by the reply deadline.
	time.Sleep(250 * time.Millisecond)
	if err := a.ConfirmAction(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, done); o.err != nil {
		t.Fatal(o.err)
	}

	done, _ = dispatch("again")
	waitPending(t, a)
	if err := h.SetTakeover(ctx, true, "test"); err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, done); o.err == nil {
		t.Fatal("takeover did not stop the waiting action")
	}
	if got, _ := os.ReadFile(a.path + ".typed"); string(got) != "hello" {
		t.Fatalf("typed %q after takeover", got)
	}
}
