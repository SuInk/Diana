package desktopctl

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type failingDispatchStore struct {
	memoryJobStore
	fail bool
}

func (s *failingDispatchStore) SaveDesktopJobs(ctx context.Context, jobs []Job) error {
	if s.fail {
		return errors.New("injected disk failure")
	}
	return s.memoryJobStore.SaveDesktopJobs(ctx, jobs)
}
func TestMergeBlockerPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	s := &failingDispatchStore{}
	m := NewJobManager(ctx, s)
	j, err := m.Create(ctx, "test", "owner", "", JobBudget{MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.fail = true
	_, _, _, err = m.AuthorizeDispatch(ctx, Command{JobID: j.ID, Op: OpWindowsList})
	if err == nil {
		t.Fatal("dispatch authorized although its durable record could not be saved")
	}
}
func TestMergeBlockerConcurrentBudget(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &memoryJobStore{})
	j, err := m.Create(ctx, "test", "owner", "", JobBudget{MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	result := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _, e := m.AuthorizeDispatch(ctx, Command{JobID: j.ID, Op: OpWindowsList})
			result <- e
		}()
	}
	close(start)
	wg.Wait()
	close(result)
	allowed := 0
	for e := range result {
		if e == nil {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("one-step budget authorized %d simultaneous commands", allowed)
	}
}

func TestDispatchPersistenceFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s := &failingDispatchStore{}
	m := NewJobManager(ctx, s)
	j, err := m.Create(ctx, "test", "owner", "", JobBudget{MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.fail = true
	_, _, _, _ = m.AuthorizeDispatch(ctx, Command{JobID: j.ID, Op: OpWindowsList})
	got, _ := m.Get(j.ID)
	if got.Status != JobQueued || got.Budget.StepsUsed != 0 || len(got.Steps) != 0 {
		t.Fatalf("failed persistence changed job: %+v", got)
	}
	s.fail = false
	_, _, step, err := m.AuthorizeDispatch(ctx, Command{JobID: j.ID, Op: OpWindowsList, IdempotencyKey: "once"})
	if err != nil {
		t.Fatal(err)
	}
	m.RecordDispatch(ctx, j.ID, step, Command{Op: OpWindowsList}, Result{OK: true}, nil)
	got, _ = m.Get(j.ID)
	if got.Budget.StepsUsed != 1 {
		t.Fatalf("receipt double-counted budget: %d", got.Budget.StepsUsed)
	}
	skip, _, _, err := m.AuthorizeDispatch(ctx, Command{JobID: j.ID, Op: OpWindowsList, IdempotencyKey: "once"})
	if err != nil || !skip {
		t.Fatalf("completed step should remain cached after budget exhausted: skip=%t err=%v", skip, err)
	}
}

func TestDispatchUnconfirmedKeyCannotReplay(t *testing.T) {
	ctx := context.Background()
	s := &memoryJobStore{}
	m := NewJobManager(ctx, s)
	j, err := m.Create(ctx, "test", "owner", "", JobBudget{MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command{JobID: j.ID, Op: OpWindowsList, IdempotencyKey: "same"}
	if _, _, _, err := m.AuthorizeDispatch(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.AuthorizeDispatch(ctx, cmd); err == nil {
		t.Fatal("duplicate in-flight key authorized")
	}
	restarted := NewJobManager(ctx, s)
	if _, err := restarted.Resume(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := restarted.AuthorizeDispatch(ctx, cmd); err == nil {
		t.Fatal("unconfirmed durable step replayed after restart")
	}
}
