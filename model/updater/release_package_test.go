package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleasePackageUpdaterStagesVerifiedArchive(t *testing.T) {
	assetName := ExpectedReleaseAssetName(runtime.GOOS, runtime.GOARCH)
	if !strings.HasSuffix(assetName, ".tar.gz") {
		t.Skip("tar package fixture is for Unix platforms")
	}
	binaryName := expectedReleaseBinaryName(runtime.GOOS, runtime.GOARCH)
	archive := releaseTarFixture(t, assetName, binaryName)
	digest := sha256.Sum256(archive)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + assetName + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + assetName:
			_, _ = w.Write(archive)
		case "/SHA256SUMS":
			_, _ = w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installRoot := t.TempDir()
	executable := filepath.Join(installRoot, binaryName)
	frontend := filepath.Join(installRoot, "frontend-next", "dist")
	database := filepath.Join(installRoot, "data", "diana.db")
	writeUpdaterTestFile(t, executable, "old-binary", 0o700)
	writeUpdaterTestFile(t, filepath.Join(frontend, "index.html"), "old-frontend", 0o600)
	writeUpdaterTestFile(t, database, "database", 0o600)
	var planPath string
	u, err := NewReleasePackageUpdater(ReleasePackageOptions{
		CurrentVersion: "v0.4.0",
		Executable:     executable,
		FrontendDir:    frontend,
		DatabasePath:   database,
		HealthURL:      server.URL + "/api/health",
		HTTPClient:     server.Client(),
		HelperStarter: func(_, path, _ string) error {
			planPath = path
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !u.Supported() {
		t.Fatalf("release updater is unsupported: %s", u.unsupportedWhy)
	}
	result, err := u.Install(context.Background(), ReleasePackage{
		Tag:       "v0.5.0",
		Archive:   ReleaseAsset{Name: assetName, URL: server.URL + "/" + assetName},
		Checksums: ReleaseAsset{Name: "SHA256SUMS", URL: server.URL + "/SHA256SUMS"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || !result.Fetched || !result.RestartRequired || result.TargetCommit != "v0.5.0" {
		t.Fatalf("Install() = %#v", result)
	}
	plan, err := readReleaseApplyPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetVersion != "v0.5.0" || plan.ExecutablePath != executable || !regularFileExists(plan.StagedExecutable) {
		t.Fatalf("apply plan = %#v", plan)
	}
}

func TestReleasePackageUpdaterRejectsChecksumMismatch(t *testing.T) {
	assetName := ExpectedReleaseAssetName(runtime.GOOS, runtime.GOARCH)
	if !strings.HasSuffix(assetName, ".tar.gz") {
		t.Skip("tar package fixture is for Unix platforms")
	}
	binaryName := expectedReleaseBinaryName(runtime.GOOS, runtime.GOARCH)
	archive := releaseTarFixture(t, assetName, binaryName)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			_, _ = fmt.Fprintf(w, "%064d  %s\n", 0, assetName)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	installRoot := t.TempDir()
	executable := filepath.Join(installRoot, binaryName)
	frontend := filepath.Join(installRoot, "frontend-next", "dist")
	database := filepath.Join(installRoot, "data", "diana.db")
	writeUpdaterTestFile(t, executable, "old", 0o700)
	writeUpdaterTestFile(t, filepath.Join(frontend, "index.html"), "old", 0o600)
	writeUpdaterTestFile(t, database, "db", 0o600)
	u, err := NewReleasePackageUpdater(ReleasePackageOptions{
		CurrentVersion: "v0.4.0",
		Executable:     executable,
		FrontendDir:    frontend,
		DatabasePath:   database,
		HealthURL:      server.URL + "/api/health",
		HTTPClient:     server.Client(),
		HelperStarter:  func(_, _, _ string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.Install(context.Background(), ReleasePackage{
		Tag:       "v0.5.0",
		Archive:   ReleaseAsset{Name: assetName, URL: server.URL + "/package"},
		Checksums: ReleaseAsset{Name: "SHA256SUMS", URL: server.URL + "/SHA256SUMS"},
	}, false)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Install() error = %v, want ErrChecksumMismatch", err)
	}
	assertUpdaterTestContent(t, executable, "old")
}

func TestExtractReleaseArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "package.tar.gz")
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 3}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractReleaseArchive(archivePath, t.TempDir()); !errors.Is(err, ErrInvalidReleaseArchive) {
		t.Fatalf("extractReleaseArchive() error = %v", err)
	}
}

func TestValidReleaseHealthURLRequiresLoopback(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:18080/api/health",
		"http://[::1]:18080/api/health",
		"https://localhost/api/health",
	} {
		if !validReleaseHealthURL(rawURL) {
			t.Errorf("validReleaseHealthURL(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"http://example.com/api/health",
		"https://192.0.2.1/api/health",
		"file:///tmp/health",
		"http://user:pass@localhost/api/health",
	} {
		if validReleaseHealthURL(rawURL) {
			t.Errorf("validReleaseHealthURL(%q) = true", rawURL)
		}
	}
}

func TestApplyReleasePlanBacksUpAndSwitchesHealthyPackage(t *testing.T) {
	plan := releaseApplyFixture(t)
	process := &fakeReleaseProcess{}
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch:        func(releaseApplyPlan) (releaseManagedProcess, error) { return process, nil },
		health:        func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "new-binary")
	assertUpdaterTestContent(t, filepath.Join(plan.FrontendPath, "index.html"), "new-frontend")
	assertUpdaterTestContent(t, filepath.Join(plan.BackupRoot, "package", filepath.Base(plan.ExecutablePath)), "old-binary")
	assertUpdaterTestContent(t, filepath.Join(plan.BackupRoot, "database", filepath.Base(plan.DatabasePath)), "old-database")
	if !process.released || process.stopped {
		t.Fatalf("process = %#v", process)
	}
	state, ok := readReleaseState(plan.InstallRoot)
	if !ok || state.Status != "healthy" || state.TargetVersion != "v0.5.0" {
		t.Fatalf("release state = %#v, ok = %v", state, ok)
	}
}

func TestApplyReleasePlanRestoresPackageAndDatabaseAfterFailedHealthCheck(t *testing.T) {
	plan := releaseApplyFixture(t)
	processes := []*fakeReleaseProcess{{}, {}}
	launches := 0
	healthChecks := 0
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch: func(releaseApplyPlan) (releaseManagedProcess, error) {
			process := processes[launches]
			launches++
			if launches == 1 {
				if err := os.WriteFile(plan.DatabasePath, []byte("migrated-database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return process, nil
		},
		health: func(context.Context, string) error {
			healthChecks++
			if healthChecks == 1 {
				return errors.New("new version unhealthy")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("applyReleasePlan() error = %v", err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "old-binary")
	assertUpdaterTestContent(t, filepath.Join(plan.FrontendPath, "index.html"), "old-frontend")
	assertUpdaterTestContent(t, plan.DatabasePath, "old-database")
	if !processes[0].stopped || !processes[1].released {
		t.Fatalf("process states = %#v", processes)
	}
	state, ok := readReleaseState(plan.InstallRoot)
	if !ok || state.Status != "rolled_back" || !strings.Contains(state.Error, "new version unhealthy") {
		t.Fatalf("release state = %#v, ok = %v", state, ok)
	}
}

func TestApplyReleasePlanRestartsOldVersionWhenUpdatedProcessCannotLaunch(t *testing.T) {
	plan := releaseApplyFixture(t)
	oldProcess := &fakeReleaseProcess{}
	launches := 0
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch: func(releaseApplyPlan) (releaseManagedProcess, error) {
			launches++
			if launches == 1 {
				return nil, errors.New("new binary cannot start")
			}
			return oldProcess, nil
		},
		health: func(context.Context, string) error { return nil },
	})
	if !errors.Is(err, errReleaseUpdateRolledBack) {
		t.Fatalf("applyReleasePlan() error = %v, want rollback", err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "old-binary")
	assertUpdaterTestContent(t, filepath.Join(plan.FrontendPath, "index.html"), "old-frontend")
	assertUpdaterTestContent(t, plan.DatabasePath, "old-database")
	if launches != 2 || !oldProcess.released {
		t.Fatalf("launches = %d, old process = %#v", launches, oldProcess)
	}
	state, ok := readReleaseState(plan.InstallRoot)
	if !ok || state.Status != "rolled_back" || !strings.Contains(state.Error, "cannot start") {
		t.Fatalf("release state = %#v, ok = %v", state, ok)
	}
}

func TestApplyReleasePlanRestartsOldVersionWhenSwitchFails(t *testing.T) {
	plan := releaseApplyFixture(t)
	if err := os.RemoveAll(plan.StagedFrontend); err != nil {
		t.Fatal(err)
	}
	oldProcess := &fakeReleaseProcess{}
	err := applyReleasePlan(plan, releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch:        func(releaseApplyPlan) (releaseManagedProcess, error) { return oldProcess, nil },
		health:        func(context.Context, string) error { return nil },
	})
	if !errors.Is(err, errReleaseUpdateRolledBack) {
		t.Fatalf("applyReleasePlan() error = %v, want rollback", err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "old-binary")
	assertUpdaterTestContent(t, filepath.Join(plan.FrontendPath, "index.html"), "old-frontend")
	assertUpdaterTestContent(t, plan.DatabasePath, "old-database")
	if !oldProcess.released {
		t.Fatalf("old process = %#v", oldProcess)
	}
}

type fakeReleaseProcess struct {
	stopped  bool
	released bool
}

func (p *fakeReleaseProcess) Stop() error {
	p.stopped = true
	return nil
}

func (p *fakeReleaseProcess) Release() error {
	p.released = true
	return nil
}

func releaseApplyFixture(t *testing.T) releaseApplyPlan {
	t.Helper()
	installRoot := t.TempDir()
	updatesRoot := filepath.Join(installRoot, ".diana-updates")
	workRoot := filepath.Join(updatesRoot, "stage-test")
	stagedRoot := filepath.Join(workRoot, "extracted", "package")
	executable := filepath.Join(installRoot, "diana-webui-test")
	frontend := filepath.Join(installRoot, "frontend-next", "dist")
	database := filepath.Join(installRoot, "data", "diana.db")
	stagedExecutable := filepath.Join(stagedRoot, "diana-webui-test")
	stagedFrontend := filepath.Join(stagedRoot, "frontend-next", "dist")
	writeUpdaterTestFile(t, executable, "old-binary", 0o700)
	writeUpdaterTestFile(t, filepath.Join(frontend, "index.html"), "old-frontend", 0o600)
	writeUpdaterTestFile(t, database, "old-database", 0o600)
	writeUpdaterTestFile(t, stagedExecutable, "new-binary", 0o700)
	writeUpdaterTestFile(t, filepath.Join(stagedFrontend, "index.html"), "new-frontend", 0o600)
	return releaseApplyPlan{
		Schema:           1,
		ParentPID:        123,
		CurrentVersion:   "v0.4.0",
		TargetVersion:    "v0.5.0",
		InstallRoot:      installRoot,
		WorkRoot:         workRoot,
		BackupRoot:       filepath.Join(updatesRoot, "backups", "test-v0.4.0"),
		ExecutablePath:   executable,
		StagedExecutable: stagedExecutable,
		FrontendPath:     frontend,
		StagedFrontend:   stagedFrontend,
		DatabasePath:     database,
		HealthURL:        "http://127.0.0.1:18080/api/health",
		WorkingDir:       installRoot,
		LogPath:          filepath.Join(updatesRoot, "last-update.log"),
	}
}

func releaseTarFixture(t *testing.T, assetName, binaryName string) []byte {
	t.Helper()
	root := releasePackageDirectory(assetName)
	files := []struct {
		name    string
		content string
		mode    int64
	}{
		{name: root + "/" + binaryName, content: "new-binary", mode: 0o755},
		{name: root + "/frontend-next/dist/index.html", content: "new-frontend", mode: 0o644},
		{name: root + "/run.sh", content: "#!/bin/sh\n", mode: 0o755},
	}
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeUpdaterTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertUpdaterTestContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != expected {
		t.Fatalf("%s = %q, want %q", path, content, expected)
	}
}
