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
if [ "$1" = screenshot ]; then printf '\211PNG\r\n\032\nfixture'; exit 0; fi
[ "$1" = type ] && [ "$2" = 42 ] && [ "$3" = --stdin ] && [ "$#" = 3 ]
[ "$DIANA_TARGET_BUNDLE" = com.example.fixture ]
[ "$(cat)" = 'hello $(no-shell-expansion)' ]
printf '{"ok":true}'
`)
	ctx := context.Background()
	cmd := Command{WindowID: "42", ExpectedBundleID: "com.example.fixture", Text: "hello $(no-shell-expansion)"}
	shot, err := a.screenshot(ctx, cmd.WindowID, cmd.ExpectedBundleID)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Observation = shot.Observation
	if _, err = a.TypeText(ctx, cmd); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	pending, _ := a.PendingActions(ctx)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if err = a.ConfirmAction(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := a.TypeText(ctx, cmd)
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
screenshot) printf '\211PNG\r\n\032\nfixture' ;;
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
	shot, err := a.screenshot(ctx, "42", "com.example.fixture")
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command{Op: OpWindowType, WindowID: "42", ExpectedBundleID: "forged", Text: "hello", Observation: shot.Observation}
	if _, err = h.Dispatch(ctx, cmd); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	pending, _ := a.PendingActions(ctx)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if err = a.ConfirmAction(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.Dispatch(ctx, cmd)
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

func TestDesktopStopSurvivesReconnectRestartAndPolicyChange(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	reg := NewRegistry(ctx, store)
	_, _ = reg.SetPolicy(ctx, writePolicy())
	hub := NewHub(reg)
	defer hub.CloseAll()
	local := NewLocalService(hub, "")
	local.adapter = &MockAdapter{}
	local.Sync(ctx)
	if err := hub.SetTakeover(ctx, true, "human"); err != nil {
		t.Fatal(err)
	}
	local.Close()
	local.Sync(ctx)
	if hub.Ready() {
		t.Fatal("reconnect released stop")
	}
	_, _ = reg.SetPolicy(ctx, writePolicy())
	restarted := NewRegistry(ctx, store)
	if !restarted.EmergencyStop() {
		t.Fatal("restart or policy edit released stop")
	}
	if _, err := hub.Dispatch(ctx, Command{Op: OpWindowsList}); ErrorCode(err) != CodeTakeover {
		t.Fatal(err)
	}
	if err := hub.SetTakeover(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	if !hub.Ready() {
		t.Fatal("explicit release failed")
	}
}

func TestProcessRejectsStaleAndConsumedObservation(t *testing.T) {
	ctx := context.Background()
	a := processFixture(t, `case "$1" in
 screenshot) printf '\211PNG\r\n\032\n'; cat "${0}.scene" ;;
 type) printf '{"ok":true}' ;;
 esac`)
	scene := a.path + ".scene"
	if err := os.WriteFile(scene, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	shot, err := a.screenshot(ctx, "42", "app")
	if err != nil {
		t.Fatal(err)
	}
	c := Command{WindowID: "42", ExpectedBundleID: "app", Text: "hello", Observation: shot.Observation}
	if _, err = a.TypeText(ctx, c); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	pending, _ := a.PendingActions(ctx)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if err = a.ConfirmAction(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	// Approval is bound to text too; changing the intended input needs a fresh grant.
	changed := c
	changed.Text = "different"
	if _, err = a.TypeText(ctx, changed); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	if err = os.WriteFile(scene, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.TypeText(ctx, c); ErrorCode(err) != CodeStaleObservation {
		t.Fatal(err)
	}
	shot, err = a.screenshot(ctx, "42", "app")
	if err != nil {
		t.Fatal(err)
	}
	c.Observation = shot.Observation
	_, _ = a.TypeText(ctx, c)
	if err = a.ConfirmAction(ctx, actionKey(c)); err != nil {
		t.Fatal(err)
	}
	got, err := a.TypeText(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsVerification || got.Evidence == nil || got.Observation == 0 {
		t.Fatalf("missing verification evidence: %+v", got)
	}
	if _, err = a.TypeText(ctx, c); ErrorCode(err) != CodeStaleObservation {
		t.Fatal("consumed observation replayed", err)
	}
}

func TestProcessCancelledSequenceWaitDoesNotLeakGate(t *testing.T) {
	a := processFixture(t, `printf '{"windows":[]}'`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.lockSequence(ctx); err == nil {
		t.Fatal("cancelled lock succeeded")
	}
	next, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if err := a.lockSequence(next); err != nil {
		t.Fatal(err)
	}
	a.unlockSequence()
}

func TestProcessElementsScrollAndApprovalExpiry(t *testing.T) {
	ctx := context.Background()
	a := processFixture(t, `case "$1" in
 screenshot) printf '\211PNG\r\n\032\nfixture' ;;
 elements) printf '{"elements":[{"id":"0.1","role":"AXButton","label":"Test","enabled":true,"x":2,"y":3,"width":20,"height":20}]}' ;;
 element-click) [ "$3" = 0.1 ]; printf '{"ok":true}' ;;
 scroll) [ "$3" = 10 ] && [ "$4" = 12 ] && [ "$5" = 0 ] && [ "$6" = 200 ]; printf '{"ok":true}' ;;
 esac`)
	elements, err := a.Elements(ctx, Command{WindowID: "42", ExpectedBundleID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	c := Command{Op: OpWindowClick, WindowID: "42", ExpectedBundleID: "app", ElementID: "0.1", Observation: elements.Observation}
	if _, err = a.Click(ctx, c); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	if err = a.ConfirmAction(ctx, actionKey(c)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Click(ctx, c); err != nil {
		t.Fatal(err)
	}
	shot, err := a.screenshot(ctx, "42", "app")
	if err != nil {
		t.Fatal(err)
	}
	x, y := 10.0, 12.0
	c = Command{Op: OpWindowScroll, WindowID: "42", ExpectedBundleID: "app", X: &x, Y: &y, DeltaY: 200, Observation: shot.Observation}
	if _, err = a.Scroll(ctx, c); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatal(err)
	}
	pending := a.approvals[actionKey(c)]
	pending.ExpiresAt = time.Now().Add(-time.Second)
	a.approvals[pending.ID] = pending
	if err = a.ConfirmAction(ctx, pending.ID); ErrorCode(err) != CodeStaleObservation {
		t.Fatal(err)
	}
	_, _ = a.Scroll(ctx, c)
	if err = a.ConfirmAction(ctx, actionKey(c)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Scroll(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopAuditOmitsRawTextAndPixels(t *testing.T) {
	c := Command{Op: OpWindowType, Text: "sensitive draft"}
	if strings.Contains(string(auditCommand(c)), c.Text) {
		t.Fatal("typed text persisted")
	}
	data := rawJSON(ActionResult{OK: true, Evidence: &ScreenshotPayload{Data: "private-pixels", Observation: 42}})
	audit := string(auditResult(c.Op, data))
	if strings.Contains(audit, "private-pixels") || !strings.Contains(audit, "image_sha256") {
		t.Fatal(audit)
	}
}

func TestTakeoverInvalidatesApprovedAction(t *testing.T) {
	ctx := context.Background()
	a := processFixture(t, `case "$1" in
 list) printf '{"windows":[{"id":"42","bundle_id":"app","active":true}]}' ;;
 screenshot) printf '\211PNG\r\n\032\nfixture' ;;
 type) printf '{"ok":true}' ;;
 esac`)
	reg := NewRegistry(ctx, nil)
	_, _ = reg.SetPolicy(ctx, writePolicy())
	hub := NewHub(reg)
	defer hub.CloseAll()
	c, _, err := hub.AttachLocal(ctx, a, Hello{}, TokenInfo{})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := a.screenshot(ctx, "42", "app")
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command{WindowID: "42", ExpectedBundleID: "app", Text: "hello", Observation: shot.Observation}
	_, _ = a.TypeText(ctx, cmd)
	if err = a.ConfirmAction(ctx, actionKey(cmd)); err != nil {
		t.Fatal(err)
	}
	c.SetTakeover(true, "human")
	c.SetTakeover(false, "")
	if _, err = a.TypeText(ctx, cmd); ErrorCode(err) != CodeStaleObservation {
		t.Fatal("old grant survived takeover", err)
	}
	pending, _ := a.PendingActions(ctx)
	if len(pending) != 0 {
		t.Fatal("pending grant survived", pending)
	}
}

type stopFailStore struct {
	memoryStore
	fail bool
}

func (s *stopFailStore) SaveDesktopControl(ctx context.Context, doc Document) error {
	if s.fail {
		return errors.New("disk failure")
	}
	return s.memoryStore.SaveDesktopControl(ctx, doc)
}
func TestFailedPersistenceCannotReleaseStop(t *testing.T) {
	ctx := context.Background()
	store := &stopFailStore{}
	reg := NewRegistry(ctx, store)
	if err := reg.setEmergencyStop(ctx, true); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if err := reg.setEmergencyStop(ctx, false); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !reg.EmergencyStop() {
		t.Fatal("failed persistence released in-memory stop")
	}
	if !NewRegistry(ctx, store).EmergencyStop() {
		t.Fatal("failed persistence released durable stop")
	}
}
