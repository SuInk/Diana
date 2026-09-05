// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHealthyReleaseReportsBackupCleanupFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without root privileges")
	}
	plan := releaseApplyFixture(t)
	backupsRoot := filepath.Dir(plan.BackupRoot)
	t.Cleanup(func() { _ = os.Chmod(backupsRoot, 0o700) })
	process := &fakeReleaseProcess{}
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch:        func(releaseApplyPlan) (releaseManagedProcess, error) { return process, nil },
		health: func(context.Context, string) error {
			if err := os.Chmod(backupsRoot, 0o500); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	})
	if err != nil || !process.released || process.stopped {
		t.Fatalf("healthy update was treated as failed: %v %#v", err, process)
	}
	state, ok := readReleaseState(plan.InstallRoot)
	if !ok || state.Status != "healthy" || state.CleanupError == "" || state.BackupRoot != plan.BackupRoot {
		t.Fatalf("cleanup failure was not recorded: %#v", state)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "new-binary")
}

func TestInstallerBackupPruningAndSuccessfulCleanup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	_, source, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(source), "..", "..", "scripts", "install.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual installer blocks without downloading a release,
	// stopping services, or modifying anything outside the temporary fixture.
	block := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(string(script), start)
		if !ok {
			t.Fatalf("missing installer block: %s", start)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("missing installer block end: %s", end)
		}
		return start + body
	}
	prune := block("for old_backup in ", "\nbackup_dir=")
	cleanup := block("  if rm -rf -- \"$backup_dir\"; then", "\n  printf 'Service:")
	installDir := t.TempDir()
	root := filepath.Join(installDir, ".installer", "backups")
	for _, name := range []string{"20260101-old", "20260201-old", "20260301-old"} {
		writeUpdaterTestFile(t, filepath.Join(root, name, "data", "diana.db"), "old-backup", 0o600)
	}
	writeUpdaterTestFile(t, filepath.Join(installDir, "data", "diana.db"), "live-database", 0o600)
	run := func(body string) {
		t.Helper()
		cmd := exec.Command("bash", "-eu", "-c", body)
		cmd.Env = append(os.Environ(), "install_dir="+installDir, "backup_dir="+filepath.Join(root, "current"))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installer backup block: %v\n%s", err, output)
		}
	}
	run(prune)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("old backups remain: %d %v", len(entries), err)
	}
	writeUpdaterTestFile(t, filepath.Join(root, "current", "data", "diana.db"), "current-backup", 0o600)
	run(cleanup)
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("successful upgrade kept backups: %d %v", len(entries), err)
	}
	assertUpdaterTestContent(t, filepath.Join(installDir, "data", "diana.db"), "live-database")
}
