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
	t.Cleanup(func() { _ = os.Chmod(plan.BackupRoot, 0o700) })
	process := &fakeReleaseProcess{}
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch:        func(releaseApplyPlan) (releaseManagedProcess, error) { return process, nil },
		health: func(context.Context, string) error {
			if err := os.Chmod(plan.BackupRoot, 0o500); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	})
	if err != nil || !process.released || process.stopped {
		t.Fatalf("healthy update was treated as failed: %v %#v", err, process)
	}
	state, ok := readReleaseState(plan.updatesDir())
	if !ok || state.Status != "healthy" || state.CleanupError == "" || state.BackupRoot != plan.BackupRoot {
		t.Fatalf("cleanup failure was not recorded: %#v", state)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "new-binary")
}

func TestPruneReleaseBackupsKeepsRecentBackupsWithinLimit(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	name := func(age time.Duration, suffix string) string {
		return now.Add(-age).Format(releaseBackupTimeLayout) + suffix
	}
	expired := []string{name(releaseBackupRetention+time.Second, "-v0.1.0"), "manual-copy", "2026-old"}
	overflow := []string{name(releaseBackupRetention, "-v0.1.1"), name(4*time.Hour, "-v0.1.2"), name(3*time.Hour, "-v0.1.3")}
	kept := []string{name(2*time.Hour, "-v0.1.4"), name(time.Hour, "-v0.1.5")}
	for _, dir := range append(append(append([]string{}, expired...), overflow...), kept...) {
		writeUpdaterTestFile(t, filepath.Join(root, dir, "database", "diana.db"), dir, 0o600)
	}
	writeUpdaterTestFile(t, filepath.Join(root, "not-a-backup.txt"), "file", 0o600)

	if err := pruneReleaseBackups(root, releaseBackupMaxCount-1, now); err != nil {
		t.Fatal(err)
	}
	for _, dir := range append(expired, overflow...) {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Errorf("backup %s was kept: %v", dir, err)
		}
	}
	for _, dir := range kept {
		assertUpdaterTestContent(t, filepath.Join(root, dir, "database", "diana.db"), dir)
	}
	assertUpdaterTestContent(t, filepath.Join(root, "not-a-backup.txt"), "file")

	// Under the count limit, a backup exactly at the retention edge stays.
	edge := t.TempDir()
	writeUpdaterTestFile(t, filepath.Join(edge, name(releaseBackupRetention, "-v0.1.1"), "database", "diana.db"), "edge", 0o600)
	writeUpdaterTestFile(t, filepath.Join(edge, name(releaseBackupRetention+time.Second, "-v0.1.0"), "database", "diana.db"), "expired", 0o600)
	if err := pruneReleaseBackups(edge, releaseBackupMaxCount-1, now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(edge)
	if err != nil || len(entries) != 1 || entries[0].Name() != name(releaseBackupRetention, "-v0.1.1") {
		t.Fatalf("edge backups = %v, err = %v", entries, err)
	}
}

func TestInstallerBackupRetentionAndSuccessfulCleanup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
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
	prune := block("backup_cutoff=", "\nbackup_dir=")
	cleanup := block("  # The database backup is kept", "\n  printf 'Service:")
	installDir := t.TempDir()
	root := filepath.Join(installDir, ".installer", "backups")
	now := time.Now().UTC()
	name := func(age time.Duration) string { return now.Add(-age).Format(releaseBackupTimeLayout) }
	expired := []string{name(4 * 24 * time.Hour), "20260101-old", "manual-copy"}
	overflow := []string{name(6 * time.Hour), name(5 * time.Hour), name(4 * time.Hour), name(3 * time.Hour)}
	kept := []string{name(2 * time.Hour), name(time.Hour)}
	for _, dir := range append(append(append([]string{}, expired...), overflow...), kept...) {
		writeUpdaterTestFile(t, filepath.Join(root, dir, "data", "diana.db"), dir, 0o600)
	}
	writeUpdaterTestFile(t, filepath.Join(installDir, "data", "diana.db"), "live-database", 0o600)
	current := filepath.Join(root, "current")
	run := func(body string) {
		t.Helper()
		cmd := exec.Command("sh", "-eu", "-c", body)
		cmd.Env = append(os.Environ(), "install_dir="+installDir, "backup_dir="+current)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installer backup block: %v\n%s", err, output)
		}
	}
	run(prune)
	for _, dir := range append(expired, overflow...) {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Errorf("backup %s was kept: %v", dir, err)
		}
	}
	for _, dir := range kept {
		assertUpdaterTestContent(t, filepath.Join(root, dir, "data", "diana.db"), dir)
	}
	writeUpdaterTestFile(t, filepath.Join(current, "runtime", "diana"), "old-binary", 0o700)
	writeUpdaterTestFile(t, filepath.Join(current, "data", "diana.db"), "current-backup", 0o600)
	run(cleanup)
	if _, err := os.Stat(filepath.Join(current, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("successful upgrade kept replaced program files: %v", err)
	}
	assertUpdaterTestContent(t, filepath.Join(current, "data", "diana.db"), "current-backup")
	assertUpdaterTestContent(t, filepath.Join(installDir, "data", "diana.db"), "live-database")
}
