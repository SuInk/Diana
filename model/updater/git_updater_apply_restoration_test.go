// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGitUpdaterStatusRejectsNonRepository(t *testing.T) {
	u, err := NewGitUpdater(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.Status(context.Background())
	if !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("Status() error = %v, want ErrRepositoryNotFound", err)
	}
}

func TestGitUpdaterCheckFetchesRemoteState(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	u, err := NewGitUpdater(repo.work)
	if err != nil {
		t.Fatal(err)
	}
	repo.commitRemote(t, "remote update")

	before, err := u.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Behind != 0 {
		t.Fatalf("status before fetch behind = %d, want 0", before.Behind)
	}
	after, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Behind != 1 || !after.UpdateAvailable || after.LastFetchedAt.IsZero() || after.Updating {
		t.Fatalf("status after fetch = %#v", after)
	}
}

func TestGitUpdaterFastForwardAndAlreadyCurrent(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	target := repo.commitRemote(t, "remote update")
	u, err := NewGitUpdater(repo.work)
	if err != nil {
		t.Fatal(err)
	}

	result, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fetched || !result.Updated || !result.SourceUpdated || result.Applied || result.RestartRequired {
		t.Fatalf("first Update() = %#v", result)
	}
	if result.TargetCommit != target || restoredGitOutput(t, repo.work, "rev-parse", "HEAD") != target {
		t.Fatalf("target commit = %q, result = %#v", target, result)
	}

	current, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Updated || current.SourceUpdated || current.Applied || !strings.Contains(current.Output, "Already up to date") {
		t.Fatalf("second Update() = %#v", current)
	}
}

func TestGitUpdaterFastForwardsFeatureBranchToReleaseTag(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	restoredGitRun(t, repo.work, "checkout", "-b", "feature/update-test")
	restoredGitRun(t, repo.work, "push", "-u", "origin", "feature/update-test")
	target := repo.commitRemote(t, "released update")
	restoredGitRun(t, repo.seed, "tag", "v1.3.0", target)
	restoredGitRun(t, repo.seed, "push", "origin", "v1.3.0")

	u, err := NewGitUpdater(repo.work)
	if err != nil {
		t.Fatal(err)
	}
	result, err := u.UpdateToRelease(context.Background(), "v1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.TargetCommit != target || result.Status.Branch != "feature/update-test" {
		t.Fatalf("UpdateToRelease() = %#v", result)
	}
	if head := restoredGitOutput(t, repo.work, "rev-parse", "HEAD"); head != target {
		t.Fatalf("HEAD = %s, want release %s", head, target)
	}
	if tag := restoredGitOutput(t, repo.work, "describe", "--tags", "--exact-match"); tag != "v1.3.0" {
		t.Fatalf("exact tag = %q", tag)
	}
}

func TestGitUpdaterReleaseTargetValidation(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	u, err := NewGitUpdater(repo.work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.UpdateToRelease(context.Background(), "origin/main"); err == nil {
		t.Fatal("branch ref accepted as a Release tag")
	}
	if _, err := u.UpdateToRelease(context.Background(), "v9.9.9"); !errors.Is(err, ErrReleaseTagMissing) {
		t.Fatalf("missing release error = %v", err)
	}
}

func TestGitUpdaterRejectsUnsafeRepositoryStates(t *testing.T) {
	t.Run("dirty work tree", func(t *testing.T) {
		repo := newRestoredUpdaterTestRepo(t)
		if err := os.WriteFile(filepath.Join(repo.work, "untracked.txt"), []byte("local"), 0o600); err != nil {
			t.Fatal(err)
		}
		u, _ := NewGitUpdater(repo.work)
		if _, err := u.Update(context.Background()); !errors.Is(err, ErrWorkingTreeDirty) {
			t.Fatalf("Update() error = %v, want ErrWorkingTreeDirty", err)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		repo := newRestoredUpdaterTestRepo(t)
		restoredGitRun(t, repo.work, "checkout", "--detach")
		u, _ := NewGitUpdater(repo.work)
		if _, err := u.Update(context.Background()); !errors.Is(err, ErrDetachedHead) {
			t.Fatalf("Update() error = %v, want ErrDetachedHead", err)
		}
	})

	t.Run("diverged branch", func(t *testing.T) {
		repo := newRestoredUpdaterTestRepo(t)
		repo.commitRemote(t, "remote update")
		restoredWriteAndCommit(t, repo.work, "state.txt", "local update", "local update")
		u, _ := NewGitUpdater(repo.work)
		if _, err := u.Update(context.Background()); !errors.Is(err, ErrNonFastForward) {
			t.Fatalf("Update() error = %v, want ErrNonFastForward", err)
		}
	})
}

func TestGitUpdaterRunsApplyCommandAndRequiresRestart(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	marker := filepath.Join(t.TempDir(), "apply.txt")
	t.Setenv("DIANA_UPDATER_HELPER", "1")
	t.Setenv("DIANA_UPDATER_HELPER_MODE", "success")
	t.Setenv("DIANA_UPDATER_HELPER_MARKER", marker)
	u := newRestoredHelperUpdater(t, repo.work)

	result, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Updated || !result.RestartRequired || !result.Status.RestartRequired {
		t.Fatalf("Update() = %#v", result)
	}
	if !strings.Contains(result.Output, "apply complete") {
		t.Fatalf("Update() output = %q", result.Output)
	}
	markerText, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markerText), repo.work) || !strings.Contains(string(markerText), result.TargetCommit) {
		t.Fatalf("apply environment marker = %q", markerText)
	}
}

func TestGitUpdaterApplyFailureCanBeRetried(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	failOnce := filepath.Join(t.TempDir(), "fail-once")
	t.Setenv("DIANA_UPDATER_HELPER", "1")
	t.Setenv("DIANA_UPDATER_HELPER_MODE", "fail-once")
	t.Setenv("DIANA_UPDATER_HELPER_MARKER", failOnce)
	u := newRestoredHelperUpdater(t, repo.work)

	if _, err := u.Update(context.Background()); !errors.Is(err, ErrApplyFailed) {
		t.Fatalf("first Update() error = %v, want ErrApplyFailed", err)
	}
	status, err := u.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.RestartRequired {
		t.Fatalf("status after failed apply = %#v", status)
	}
	result, err := u.Update(context.Background())
	if err != nil {
		t.Fatalf("retry Update() error = %v", err)
	}
	if !result.Applied || !result.RestartRequired || !strings.Contains(result.Output, "retry complete") {
		t.Fatalf("retry Update() = %#v", result)
	}
}

func TestGitUpdaterRejectsConcurrentUpdate(t *testing.T) {
	repo := newRestoredUpdaterTestRepo(t)
	temp := t.TempDir()
	started := filepath.Join(temp, "started")
	release := filepath.Join(temp, "release")
	t.Setenv("DIANA_UPDATER_HELPER", "1")
	t.Setenv("DIANA_UPDATER_HELPER_MODE", "block")
	t.Setenv("DIANA_UPDATER_HELPER_MARKER", started)
	t.Setenv("DIANA_UPDATER_HELPER_RELEASE", release)
	u := newRestoredHelperUpdater(t, repo.work)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, err := u.Update(ctx)
		firstDone <- err
	}()
	restoredWaitForFile(t, started, 5*time.Second)
	status, err := u.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Updating {
		t.Fatal("Status().Updating = false while apply command is blocked")
	}
	if _, err := u.Update(context.Background()); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("concurrent Update() error = %v, want ErrUpdateInProgress", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first Update() error = %v", err)
	}
}

func TestApplyUpdateScriptBuildsFrontendNextAndKeepsBackups(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	script := filepath.Join(repositoryRoot, "scripts", "apply-update.sh")
	fixtureRoot := t.TempDir()
	sourceRoot := filepath.Join(fixtureRoot, "source")
	frontendBin := filepath.Join(sourceRoot, "frontend-next", "node_modules", ".bin")
	binDir := filepath.Join(fixtureRoot, "bin")
	executable := filepath.Join(fixtureRoot, "application", "diana-webui")
	frontend := filepath.Join(fixtureRoot, "web", "dist")
	for _, path := range []string{sourceRoot, frontendBin, binDir, filepath.Dir(executable), frontend} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	restoredGitRun(t, sourceRoot, "init")
	if err := os.WriteFile(filepath.Join(sourceRoot, "frontend-next", "package-lock.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("old-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "index.html"), []byte("old-frontend"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRestoredExecutable(t, filepath.Join(binDir, "npm"), "#!/bin/sh\nexit 0\n")
	writeRestoredExecutable(t, filepath.Join(binDir, "uname"), "#!/bin/sh\nprintf 'Linux\\n'\n")
	writeRestoredExecutable(t, filepath.Join(frontendBin, "vue-tsc"), "#!/bin/sh\nexit 0\n")
	writeRestoredExecutable(t, filepath.Join(frontendBin, "vite"), `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--outDir" ]; then shift; out="$1"; fi
  shift
done
mkdir -p "$out"
printf 'new-frontend' > "$out/index.html"
`)
	writeRestoredExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'new-binary' > "$out"
chmod 700 "$out"
`)

	cmd := exec.Command("bash", script)
	cmd.Env = environmentWithOverrides(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DIANA_UPDATE_ROOT="+sourceRoot,
		"DIANA_UPDATE_TARGET_COMMIT=0123456789abcdef",
		"DIANA_RUNNING_EXECUTABLE="+executable,
		"FRONTEND_DIST="+frontend,
		"GO="+filepath.Join(binDir, "go"),
		"NPM="+filepath.Join(binDir, "npm"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-update.sh: %v\n%s", err, output)
	}
	assertRestoredFileContent(t, executable, "new-binary")
	assertRestoredFileContent(t, executable+".backup", "old-binary")
	assertRestoredFileContent(t, filepath.Join(frontend, "index.html"), "new-frontend")
	assertRestoredFileContent(t, filepath.Join(frontend+".backup", "index.html"), "old-frontend")
}

func TestGitUpdaterRestoredApplyHelper(t *testing.T) {
	if os.Getenv("DIANA_UPDATER_HELPER") != "1" {
		return
	}
	marker := os.Getenv("DIANA_UPDATER_HELPER_MARKER")
	switch os.Getenv("DIANA_UPDATER_HELPER_MODE") {
	case "success":
		content := os.Getenv("DIANA_UPDATE_ROOT") + "\n" + os.Getenv("DIANA_UPDATE_TARGET_COMMIT") + "\n" + os.Getenv("DIANA_RUNNING_EXECUTABLE")
		if err := os.WriteFile(marker, []byte(content), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("apply complete")
		os.Exit(0)
	case "fail-once":
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(marker, []byte("failed"), 0o600)
			fmt.Fprintln(os.Stderr, "intentional apply failure")
			os.Exit(3)
		}
		fmt.Println("retry complete")
		os.Exit(0)
	case "block":
		if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
			os.Exit(2)
		}
		release := os.Getenv("DIANA_UPDATER_HELPER_RELEASE")
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(release); err == nil {
				fmt.Println("block released")
				os.Exit(0)
			}
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprintln(os.Stderr, "timed out waiting for release")
		os.Exit(4)
	default:
		os.Exit(5)
	}
}

type restoredUpdaterTestRepo struct {
	remote string
	seed   string
	work   string
}

func newRestoredUpdaterTestRepo(t *testing.T) restoredUpdaterTestRepo {
	t.Helper()
	root := t.TempDir()
	repo := restoredUpdaterTestRepo{
		remote: filepath.Join(root, "remote.git"),
		seed:   filepath.Join(root, "seed"),
		work:   filepath.Join(root, "work"),
	}
	restoredGitRun(t, root, "init", "--bare", repo.remote)
	restoredGitRun(t, root, "init", repo.seed)
	restoredGitRun(t, repo.seed, "checkout", "-b", "main")
	restoredConfigureGit(t, repo.seed)
	restoredWriteAndCommit(t, repo.seed, "state.txt", "initial", "initial")
	restoredGitRun(t, repo.seed, "remote", "add", "origin", repo.remote)
	restoredGitRun(t, repo.seed, "push", "-u", "origin", "main")
	restoredGitRun(t, repo.remote, "symbolic-ref", "HEAD", "refs/heads/main")
	restoredGitRun(t, root, "clone", repo.remote, repo.work)
	restoredConfigureGit(t, repo.work)
	return repo
}

func (r restoredUpdaterTestRepo) commitRemote(t *testing.T, content string) string {
	t.Helper()
	restoredWriteAndCommit(t, r.seed, "state.txt", content, content)
	restoredGitRun(t, r.seed, "push", "origin", "main")
	return restoredGitOutput(t, r.seed, "rev-parse", "HEAD")
}

func newRestoredHelperUpdater(t *testing.T, root string) *GitUpdater {
	t.Helper()
	u, err := NewGitUpdaterWithOptions(root, Options{
		ApplyCommand:      []string{os.Args[0], "-test.run=^TestGitUpdaterRestoredApplyHelper$"},
		RunningCommit:     "0000000",
		RunningExecutable: filepath.Join(root, "diana-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func restoredConfigureGit(t *testing.T, dir string) {
	t.Helper()
	restoredGitRun(t, dir, "config", "user.name", "Diana Updater Test")
	restoredGitRun(t, dir, "config", "user.email", "updater@example.test")
}

func restoredWriteAndCommit(t *testing.T, dir, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	restoredGitRun(t, dir, "add", name)
	restoredGitRun(t, dir, "commit", "-m", message)
}

func restoredGitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = restoredGitOutput(t, dir, args...)
}

func restoredGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func restoredWaitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func writeRestoredExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertRestoredFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(content) != expected {
		t.Fatalf("%s = %q, want %q", path, content, expected)
	}
}
