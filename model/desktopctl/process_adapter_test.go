package desktopctl

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func processFixture(t *testing.T, script string) *ProcessAdapter {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewProcessAdapter(path)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestProcessAdapterInputAndIdentity(t *testing.T) {
	a := processFixture(t, `
[ "$1" = type ] && [ "$2" = 42 ] && [ "$3" = --stdin ] && [ "$#" = 3 ]
[ "$DIANA_TARGET_BUNDLE" = com.example.fixture ]
[ "$(cat)" = 'hello $(no-shell-expansion)' ]
printf '{"ok":true}'
`)
	got, err := a.TypeText(context.Background(), Command{WindowID: "42", ExpectedBundleID: "com.example.fixture", Text: "hello $(no-shell-expansion)"})
	if err != nil || !got.OK || got.WindowID != "42" || got.BundleID != "com.example.fixture" {
		t.Fatalf("receipt=%+v err=%v", got, err)
	}
}

func TestProcessAdapterCancellationReleasesGate(t *testing.T) {
	a := processFixture(t, `if [ "$1" = wait ]; then exec sleep 30; fi
printf '{"windows":[]}'`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.run(ctx, "", "", "wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation: %v", err)
	}
	next, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if _, err := a.ListWindows(next); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdapterPermissionAndInvalidOutput(t *testing.T) {
	a := processFixture(t, `printf 'permission_denied: Screen Recording\n' >&2; exit 1`)
	if _, err := a.Screenshot(context.Background(), "42"); ErrorCode(err) != CodePermissionDenied {
		t.Fatalf("%v", err)
	}
	a = processFixture(t, `printf 'not PNG'`)
	if _, err := a.Screenshot(context.Background(), "42"); ErrorCode(err) != CodeHelper {
		t.Fatalf("%v", err)
	}
	b := &boundedOutput{limit: 3}
	if _, err := b.Write([]byte("abcd")); err == nil || b.Len() != 0 {
		t.Fatal("output limit bypassed")
	}
}

func TestLocalProcessRefreshAndTrustedTarget(t *testing.T) {
	a := processFixture(t, `
case "$1" in
list) printf '{"windows":[{"id":"42","app_name":"Fixture","bundle_id":"com.example.fixture","active":true}]}' ;;
type) [ "$DIANA_TARGET_BUNDLE" = com.example.fixture ]; printf '{"ok":true}' ;;
esac`)
	ctx := context.Background()
	r := NewRegistry(ctx, nil)
	_, _ = r.SetPolicy(ctx, writePolicy())
	h := NewHub(r)
	defer h.CloseAll()
	c, _, err := h.AttachLocal(ctx, a, Hello{}, TokenInfo{})
	if err != nil {
		t.Fatal(err)
	}
	PublishWindows(c, nil)
	_, err = h.Dispatch(ctx, Command{Op: OpWindowType, WindowID: "42", ExpectedBundleID: "forged", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	c.SetTakeover(true, "test")
	if _, err = h.Dispatch(ctx, Command{Op: OpWindowType, WindowID: "42", Text: "hello"}); ErrorCode(err) != CodeTakeover {
		t.Fatal(err)
	}
}

func TestLocalServiceReconnectAndDisable(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(ctx, nil)
	h := NewHub(r)
	s := NewLocalService(h, "")
	s.adapter = &MockAdapter{}
	defer s.Close()
	s.Sync(ctx)
	if h.Ready() {
		t.Fatal("connected while disabled")
	}
	_, _ = r.SetPolicy(ctx, writePolicy())
	s.Sync(ctx)
	if !h.Ready() {
		t.Fatal("not connected")
	}
	h.CloseAll()
	s.Sync(ctx)
	if !h.Ready() {
		t.Fatal("did not reconnect")
	}
	_, _ = r.SetPolicy(ctx, Policy{})
	s.Sync(ctx)
	if len(h.Connections()) != 0 {
		t.Fatal("connection survives disable")
	}
}

func TestLocalTakeoverRejectsLateCommand(t *testing.T) {
	c := &LocalConn{Adapter: &MockAdapter{}}
	if err := c.Send(Frame{Type: FrameTakeover, Data: rawJSON(TakeoverPayload{Active: true})}); err != nil {
		t.Fatal(err)
	}
	err := c.Send(Frame{Type: FrameCommand, Op: OpWindowType})
	if ErrorCode(err) != CodeTakeover {
		t.Fatal(err)
	}
}

func TestJobCannotBypassConfirmationThroughPause(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, nil)
	j, err := m.Create(ctx, "test", "owner", "", JobBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.WaitConfirm(ctx, j.ID, "human required"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Pause(ctx, j.ID, ""); err == nil {
		t.Fatal("confirmation can be cleared via pause")
	}
	if _, err = m.Resume(ctx, j.ID); err == nil {
		t.Fatal("confirmation can be cleared via resume")
	}
	if _, err = m.Confirm(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRefreshRejectsReusedWindow(t *testing.T) {
	a := processFixture(t, `printf '{"windows":[{"id":"42","bundle_id":"com.example.denied"}]}'`)
	ctx := context.Background()
	r := NewRegistry(ctx, nil)
	p := writePolicy()
	p.AllowedApps = []string{"com.example.allowed"}
	_, _ = r.SetPolicy(ctx, p)
	h := NewHub(r)
	defer h.CloseAll()
	c, _, err := h.AttachLocal(ctx, a, Hello{}, TokenInfo{})
	if err != nil {
		t.Fatal(err)
	}
	PublishWindows(c, []WindowInfo{{ID: "42", BundleID: "com.example.allowed"}})
	_, err = h.Dispatch(ctx, Command{Op: OpWindowType, WindowID: "42", Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatal("stale allowed window was used", err)
	}
}

type blockedProcessAdapter struct {
	MockAdapter
	started chan struct{}
	stopped chan struct{}
}

func (a *blockedProcessAdapter) TypeText(ctx context.Context, c Command) (ActionResult, error) {
	close(a.started)
	<-ctx.Done()
	close(a.stopped)
	return ActionResult{}, ctx.Err()
}
func TestJobCancellationReachesLocalExecutor(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(ctx, nil)
	_, _ = r.SetPolicy(ctx, writePolicy())
	h := NewHub(r)
	defer h.CloseAll()
	jobs := NewJobManager(ctx, nil)
	h.SetJobManager(jobs)
	a := &blockedProcessAdapter{MockAdapter: MockAdapter{Windows: []WindowInfo{{ID: "42", Active: true}}}, started: make(chan struct{}), stopped: make(chan struct{})}
	if _, _, err := h.AttachLocal(ctx, a, Hello{}, TokenInfo{}); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.Create(ctx, "test", "owner", "", JobBudget{})
	result := make(chan error, 1)
	go func() {
		_, err := h.Dispatch(ctx, Command{Op: OpWindowType, WindowID: "42", Text: "test", JobID: job.ID})
		result <- err
	}()
	select {
	case <-a.started:
	case <-time.After(time.Second):
		t.Fatal("executor never started")
	}
	if _, err := jobs.Cancel(ctx, job.ID, "test cancellation"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.stopped:
	case <-time.After(time.Second):
		t.Fatal("executor was not cancelled")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled dispatch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch did not return")
	}
}

func TestRestartPreservesHumanConfirmation(t *testing.T) {
	ctx := context.Background()
	store := &memoryJobStore{}
	m := NewJobManager(ctx, store)
	job, _ := m.Create(ctx, "test", "owner", "", JobBudget{})
	_, _ = m.WaitConfirm(ctx, job.ID, "human required")
	restarted := NewJobManager(ctx, store)
	got, _ := restarted.Get(job.ID)
	if got.Status != JobWaitingConfirm {
		t.Fatalf("restart lost confirmation: %s", got.Status)
	}
	if _, err := restarted.Resume(ctx, job.ID); err == nil {
		t.Fatal("restart bypassed confirmation")
	}
}

func TestOldDispatchCannotUnbindNewCancellation(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, nil)
	job, _ := m.Create(ctx, "test", "owner", "", JobBudget{})
	_, _ = m.Start(ctx, job.ID)
	_, oldCancel := context.WithCancel(ctx)
	defer oldCancel()
	m.BindCancel(job.ID, "old", oldCancel)
	newCtx, newCancel := context.WithCancel(ctx)
	defer newCancel()
	m.BindCancel(job.ID, "new", newCancel)
	m.UnbindCancel(job.ID, "old")
	_, _ = m.Cancel(ctx, job.ID, "cancel new")
	if newCtx.Err() == nil {
		t.Fatal("old completion removed the current cancellation")
	}
}

func TestHelperOutputCopyCannotBypassLimit(t *testing.T) {
	b := &boundedOutput{limit: 3}
	_, err := io.Copy(b, io.LimitReader(strings.NewReader("too much output"), 100))
	if err == nil || b.Len() > 3 {
		t.Fatal("io.Copy bypassed helper output limit")
	}
}
