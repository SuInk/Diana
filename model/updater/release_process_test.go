// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestUpdateParentExitEscalation(t *testing.T) {
	denied := errors.New("permission denied")
	for _, tc := range []struct {
		name                   string
		first, killErr, second error
		wantKill               bool
		wantErr                error
	}{
		{name: "graceful"},
		{name: "query failure", first: denied, wantErr: denied},
		{name: "hung shutdown", first: fmt.Errorf("wrapped: %w", errProcessExitTimeout), wantKill: true},
		{name: "exited before kill", first: errProcessExitTimeout, killErr: os.ErrProcessDone, wantKill: true},
		{name: "kill failure", first: errProcessExitTimeout, killErr: denied, wantKill: true, wantErr: denied},
		{name: "still running", first: errProcessExitTimeout, second: errProcessExitTimeout, wantKill: true, wantErr: errProcessExitTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waits, kills := 0, 0
			err := waitForUpdateParentExitWith(os.Getpid()+10000, 15*time.Second, func(_ int, timeout time.Duration) error {
				waits++
				if waits == 1 {
					if timeout != 15*time.Second {
						t.Fatalf("grace period = %s", timeout)
					}
					return tc.first
				}
				if timeout != 5*time.Second {
					t.Fatalf("kill wait = %s", timeout)
				}
				return tc.second
			}, func(int) error { kills++; return tc.killErr })
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if (kills == 1) != tc.wantKill {
				t.Fatalf("kill calls = %d", kills)
			}
			wantWaits := 1
			if tc.wantKill && (tc.killErr == nil || errors.Is(tc.killErr, os.ErrProcessDone)) {
				wantWaits = 2
			}
			if waits != wantWaits {
				t.Fatalf("wait calls = %d, want %d", waits, wantWaits)
			}
		})
	}
}

func TestUpdateParentRejectsUnsafePID(t *testing.T) {
	for _, pid := range []int{-1, 0, 1, os.Getpid()} {
		err := waitForUpdateParentExitWith(pid, time.Second, func(int, time.Duration) error {
			t.Fatal("unsafe PID reached wait")
			return nil
		}, func(int) error { t.Fatal("unsafe PID reached kill"); return nil })
		if err == nil {
			t.Fatalf("accepted pid %d", pid)
		}
	}
}

// 用真实子进程模拟永不结束的退出流程，验证强杀后确实退出，非仅发出信号。
func TestUpdateParentForceStopsProcess(t *testing.T) {
	if os.Getenv("DIANA_TEST_HUNG_UPDATE_PARENT") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUpdateParentForceStopsProcess$")
	cmd.Env = append(os.Environ(), "DIANA_TEST_HUNG_UPDATE_PARENT=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if err := waitForUpdateParentExit(cmd.Process.Pid, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hung child unexpectedly exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after forced termination")
	}
}

func TestApplyReleasePlanStopsBeforeReplacingFilesWhenParentSurvives(t *testing.T) {
	plan := releaseApplyFixture(t)
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(_ int, timeout time.Duration) error {
			if timeout != 15*time.Second {
				t.Fatalf("grace period = %s", timeout)
			}
			return errProcessExitTimeout
		},
		launch: func(releaseApplyPlan) (releaseManagedProcess, error) {
			t.Fatal("launched with old process alive")
			return nil, nil
		},
		health: func(context.Context, string) error { t.Fatal("checked health with old process alive"); return nil },
	})
	if !errors.Is(err, errProcessExitTimeout) {
		t.Fatalf("error = %v", err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "old-binary")
	assertUpdaterTestContent(t, plan.DatabasePath, "old-database")
	if _, err := os.Stat(plan.BackupRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup started before parent exit: %v", err)
	}
}
