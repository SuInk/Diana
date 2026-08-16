package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	ErrReleaseUpdateUnsupported = errors.New("updater: release package self-update is not supported by this deployment")
	ErrReleaseAssetMissing      = errors.New("updater: release package asset is missing")
	ErrChecksumMissing          = errors.New("updater: SHA-256 checksum is missing")
	ErrChecksumMismatch         = errors.New("updater: release package SHA-256 mismatch")
	ErrInvalidReleaseArchive    = errors.New("updater: invalid release package archive")
)

const (
	InternalReleaseApplyCommand = "__diana_apply_release"
	maxChecksumBytes            = 1 << 20
	maxReleasePackageBytes      = 512 << 20
	maxExtractedPackageBytes    = 1 << 30
	maxExtractedPackageFiles    = 20_000
)

// ReleaseAsset describes one downloadable GitHub Release asset.
type ReleaseAsset struct {
	Name string
	URL  string
	Size int64
}

// ReleasePackage contains the platform package and its checksum manifest.
type ReleasePackage struct {
	Tag       string
	Archive   ReleaseAsset
	Checksums ReleaseAsset
}

type pendingReleaseUpdate struct {
	Schema        int       `json:"schema"`
	TargetVersion string    `json:"target_version"`
	PlanPath      string    `json:"plan_path"`
	DownloadedAt  time.Time `json:"downloaded_at"`
}

// ReleasePackageOptions describes the currently running complete Release package.
type ReleasePackageOptions struct {
	CurrentVersion string
	Executable     string
	FrontendDir    string
	DatabasePath   string
	HealthURL      string
	WorkingDir     string
	Arguments      []string
	HTTPClient     *http.Client
	Shutdown       func()
	Disable        bool

	// PlatformOS, PlatformArch, and HelperStarter are test seams. Production
	// callers leave them empty.
	PlatformOS    string
	PlatformArch  string
	HelperStarter func(executable, planPath, logPath string) error
}

// ReleasePackageUpdater downloads and stages complete Release packages. A
// detached copy of the running executable performs the actual switch after
// the HTTP response has been returned and the old process has shut down.
type ReleasePackageUpdater struct {
	currentVersion string
	executable     string
	frontendDir    string
	databasePath   string
	healthURL      string
	workingDir     string
	arguments      []string
	installRoot    string
	assetName      string
	binaryName     string
	httpClient     *http.Client
	shutdown       func()
	startHelper    func(executable, planPath, logPath string) error
	supported      bool
	unsupportedWhy string

	operationMu     sync.Mutex
	handoffStarted  bool
	progressMu      sync.RWMutex
	operationActive bool
	progress        releaseDownloadProgress
}

type releaseDownloadProgress struct {
	phase string
	done  int64
	total int64
}

func (u *ReleasePackageUpdater) setProgress(phase string, done, total int64) {
	u.progressMu.Lock()
	u.progress = releaseDownloadProgress{phase: phase, done: done, total: total}
	u.progressMu.Unlock()
}

func (u *ReleasePackageUpdater) beginOperation() bool {
	if !u.operationMu.TryLock() {
		return false
	}
	u.progressMu.Lock()
	u.operationActive = true
	u.progressMu.Unlock()
	return true
}

func (u *ReleasePackageUpdater) endOperation() {
	u.progressMu.Lock()
	u.operationActive = false
	u.progressMu.Unlock()
	u.operationMu.Unlock()
}

// ExpectedReleaseAssetName returns the complete-package asset for a platform.
func ExpectedReleaseAssetName(goos, goarch string) string {
	base := "diana-webui-" + strings.TrimSpace(goos) + "-" + strings.TrimSpace(goarch)
	if goos == "windows" {
		return base + ".zip"
	}
	return base + ".tar.gz"
}

func expectedReleaseBinaryName(goos, _ string) string {
	name := "diana-webui"
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func legacyReleaseBinaryName(goos, goarch string) string {
	name := "diana-webui-" + strings.TrimSpace(goos) + "-" + strings.TrimSpace(goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// NewReleasePackageUpdater detects whether the current process is running from
// a complete Release package. Unsupported layouts return a usable updater whose
// Status method reports ErrReleaseUpdateUnsupported; this keeps source and
// container deployments non-fatal.
func NewReleasePackageUpdater(options ReleasePackageOptions) (*ReleasePackageUpdater, error) {
	goos := strings.TrimSpace(options.PlatformOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(options.PlatformArch)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	absExecutable, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	installRoot := filepath.Dir(absExecutable)
	binaryName := expectedReleaseBinaryName(goos, goarch)
	legacyBinaryName := legacyReleaseBinaryName(goos, goarch)
	if filepath.Base(absExecutable) == legacyBinaryName {
		binaryName = legacyBinaryName
	}
	frontendDir := strings.TrimSpace(options.FrontendDir)
	if frontendDir == "" {
		frontendDir = filepath.Join(installRoot, "frontend-next", "dist")
	}
	absFrontend, err := filepath.Abs(frontendDir)
	if err != nil {
		return nil, err
	}
	databasePath := strings.TrimSpace(options.DatabasePath)
	if databasePath != "" {
		databasePath, err = filepath.Abs(databasePath)
		if err != nil {
			return nil, err
		}
	}
	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return nil, err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	starter := options.HelperStarter
	if starter == nil {
		starter = startDetachedReleaseHelper
	}
	u := &ReleasePackageUpdater{
		currentVersion: strings.TrimSpace(options.CurrentVersion),
		executable:     absExecutable,
		frontendDir:    absFrontend,
		databasePath:   databasePath,
		healthURL:      strings.TrimSpace(options.HealthURL),
		workingDir:     workingDir,
		arguments:      append([]string(nil), options.Arguments...),
		installRoot:    installRoot,
		assetName:      ExpectedReleaseAssetName(goos, goarch),
		binaryName:     binaryName,
		httpClient:     client,
		shutdown:       options.Shutdown,
		startHelper:    starter,
	}
	switch {
	case options.Disable:
		u.unsupportedWhy = "deployment explicitly disabled package replacement"
	case goos != "darwin" && goos != "linux" && goos != "windows":
		u.unsupportedWhy = "unsupported operating system " + goos
	case filepath.Base(absExecutable) != u.binaryName:
		u.unsupportedWhy = fmt.Sprintf("running executable %q is not the packaged binary %q", filepath.Base(absExecutable), u.binaryName)
	case !pathWithin(installRoot, absFrontend):
		u.unsupportedWhy = "frontend directory is outside the package root"
	case !regularFileExists(filepath.Join(absFrontend, "index.html")):
		u.unsupportedWhy = "packaged frontend is missing"
	case databasePath == "" || !regularFileExists(databasePath):
		u.unsupportedWhy = "SQLite database is missing"
	case u.healthURL == "":
		u.unsupportedWhy = "health-check URL is missing"
	case !validReleaseHealthURL(u.healthURL):
		u.unsupportedWhy = "health-check URL must target loopback HTTP"
	default:
		u.supported = true
	}
	return u, nil
}

func (u *ReleasePackageUpdater) Supported() bool {
	return u != nil && u.supported
}

func (u *ReleasePackageUpdater) ExpectedAssetName() string {
	if u == nil {
		return ""
	}
	return u.assetName
}

func (u *ReleasePackageUpdater) Status(context.Context) (Status, error) {
	if !u.Supported() {
		reason := ""
		if u != nil {
			reason = u.unsupportedWhy
		}
		if reason == "" {
			return Status{}, ErrReleaseUpdateUnsupported
		}
		return Status{}, fmt.Errorf("%w: %s", ErrReleaseUpdateUnsupported, reason)
	}
	status := Status{
		Root:            u.installRoot,
		RunningCommit:   u.currentVersion,
		NearestTag:      u.currentVersion,
		ApplySupported:  true,
		UpdateAvailable: false,
	}
	u.progressMu.RLock()
	progress := u.progress
	status.Updating = u.operationActive
	u.progressMu.RUnlock()
	status.UpdatePhase = progress.phase
	status.DownloadedBytes = progress.done
	status.DownloadTotal = progress.total
	if progress.total > 0 {
		status.DownloadPercent = int(progress.done * 100 / progress.total)
		if status.DownloadPercent > 100 {
			status.DownloadPercent = 100
		}
	}
	if state, ok := readReleaseState(u.installRoot); ok {
		status.LastUpdateAt = state.At
		status.LastUpdateText = state.Status
		if state.TargetVersion != "" {
			status.LastUpdateText += ": " + state.TargetVersion
		}
		if state.Error != "" {
			status.LastUpdateText += " (" + state.Error + ")"
		}
	}
	if pending, ok := u.pendingUpdate(); ok {
		status.DownloadReady = true
		status.DownloadedVersion = pending.TargetVersion
		status.DownloadedAt = pending.DownloadedAt
	}
	return status, nil
}

// Download downloads, verifies, extracts, and stages a complete Release package
// without changing the running installation or restarting the service.
func (u *ReleasePackageUpdater) Download(ctx context.Context, release ReleasePackage, force bool) (Result, error) {
	status, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if !u.beginOperation() {
		return Result{}, ErrUpdateInProgress
	}
	defer u.endOperation()
	u.setProgress("preparing", 0, release.Archive.Size)
	defer func() {
		if _, ok := u.pendingUpdate(); ok {
			u.progressMu.RLock()
			total := u.progress.total
			u.progressMu.RUnlock()
			if total <= 0 {
				total = release.Archive.Size
			}
			u.setProgress("ready", total, total)
			return
		}
		u.setProgress("", 0, 0)
	}()
	if u.handoffStarted {
		return Result{}, ErrUpdateInProgress
	}

	release.Tag = strings.TrimSpace(release.Tag)
	if !releaseTagPattern.MatchString(release.Tag) {
		return Result{}, fmt.Errorf("updater: invalid release tag %q", release.Tag)
	}
	if release.Archive.Name != u.assetName || strings.TrimSpace(release.Archive.URL) == "" {
		return Result{}, fmt.Errorf("%w: %s", ErrReleaseAssetMissing, u.assetName)
	}
	if release.Checksums.Name != "SHA256SUMS" || strings.TrimSpace(release.Checksums.URL) == "" {
		return Result{}, ErrChecksumMissing
	}
	if !validReleaseDownloadURL(release.Archive.URL) || !validReleaseDownloadURL(release.Checksums.URL) {
		return Result{}, errors.New("updater: release asset URL must use HTTPS")
	}
	if pending, ok := u.pendingUpdate(); ok && pending.TargetVersion == release.Tag && !force {
		status.DownloadReady = true
		status.DownloadedVersion = pending.TargetVersion
		status.DownloadedAt = pending.DownloadedAt
		return Result{Status: status, Downloaded: true, TargetCommit: release.Tag, Output: "Release package is already downloaded and verified.", At: time.Now()}, nil
	}
	u.removePendingUpdate()

	updatesRoot := filepath.Join(u.installRoot, ".diana-updates")
	if err := os.MkdirAll(updatesRoot, 0o700); err != nil {
		return Result{}, fmt.Errorf("create update workspace: %w", err)
	}
	workRoot, err := os.MkdirTemp(updatesRoot, "stage-")
	if err != nil {
		return Result{}, err
	}
	keepWorkRoot := false
	defer func() {
		if !keepWorkRoot {
			_ = os.RemoveAll(workRoot)
		}
	}()

	checksumPath := filepath.Join(workRoot, "SHA256SUMS")
	u.setProgress("checksum", 0, release.Archive.Size)
	if _, err := downloadReleaseFile(ctx, u.httpClient, release.Checksums.URL, checksumPath, maxChecksumBytes, nil); err != nil {
		return Result{}, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	manifest, err := os.ReadFile(checksumPath)
	if err != nil {
		return Result{}, err
	}
	wantDigest, err := checksumForAsset(manifest, u.assetName)
	if err != nil {
		return Result{}, err
	}
	archivePath := filepath.Join(workRoot, u.assetName)
	u.setProgress("downloading", 0, release.Archive.Size)
	gotDigest, err := downloadReleaseFile(ctx, u.httpClient, release.Archive.URL, archivePath, maxReleasePackageBytes, func(done, total int64) {
		u.setProgress("downloading", done, total)
	})
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", u.assetName, err)
	}
	if !strings.EqualFold(gotDigest, wantDigest) {
		return Result{}, fmt.Errorf("%w: %s expected %s, got %s", ErrChecksumMismatch, u.assetName, wantDigest, gotDigest)
	}
	archiveSize := release.Archive.Size
	if info, statErr := os.Stat(archivePath); statErr == nil && archiveSize <= 0 {
		archiveSize = info.Size()
	}
	u.setProgress("extracting", archiveSize, archiveSize)

	extractRoot := filepath.Join(workRoot, "extracted")
	if err := os.Mkdir(extractRoot, 0o700); err != nil {
		return Result{}, err
	}
	if err := extractReleaseArchive(archivePath, extractRoot); err != nil {
		return Result{}, err
	}
	packageRoot := filepath.Join(extractRoot, releasePackageDirectory(u.assetName))
	stagedExecutable := filepath.Join(packageRoot, u.binaryName)
	stagedFrontend := filepath.Join(packageRoot, "frontend-next", "dist")
	if !regularFileExists(stagedExecutable) || !regularFileExists(filepath.Join(stagedFrontend, "index.html")) {
		return Result{}, fmt.Errorf("%w: expected binary or frontend is missing", ErrInvalidReleaseArchive)
	}

	helperPath := filepath.Join(workRoot, "diana-release-helper")
	if runtime.GOOS == "windows" {
		helperPath += ".exe"
	}
	if err := copyRegularFile(u.executable, helperPath, 0o700); err != nil {
		return Result{}, fmt.Errorf("stage update helper: %w", err)
	}
	backupName := time.Now().UTC().Format("20060102T150405Z") + "-" + safePathComponent(u.currentVersion)
	plan := releaseApplyPlan{
		Schema:           1,
		ParentPID:        os.Getpid(),
		CurrentVersion:   u.currentVersion,
		TargetVersion:    release.Tag,
		InstallRoot:      u.installRoot,
		WorkRoot:         workRoot,
		BackupRoot:       filepath.Join(updatesRoot, "backups", backupName),
		ExecutablePath:   u.executable,
		StagedExecutable: stagedExecutable,
		FrontendPath:     u.frontendDir,
		StagedFrontend:   stagedFrontend,
		DatabasePath:     u.databasePath,
		HealthURL:        u.healthURL,
		WorkingDir:       u.workingDir,
		Arguments:        append([]string(nil), u.arguments...),
		LogPath:          filepath.Join(updatesRoot, "last-update.log"),
	}
	for _, name := range []string{"run.sh", "run.bat", "README.txt"} {
		if regularFileExists(filepath.Join(packageRoot, name)) {
			plan.OptionalFiles = append(plan.OptionalFiles, releaseApplyFile{
				Target: filepath.Join(u.installRoot, name),
				Staged: filepath.Join(packageRoot, name),
			})
		}
	}
	planPath := filepath.Join(workRoot, "apply-plan.json")
	if err := validateReleaseApplyPlan(plan); err != nil {
		return Result{}, err
	}
	if err := writePrivateJSON(planPath, plan); err != nil {
		return Result{}, err
	}
	downloadedAt := time.Now()
	pending := pendingReleaseUpdate{Schema: 1, TargetVersion: release.Tag, PlanPath: planPath, DownloadedAt: downloadedAt}
	if err := writePrivateJSON(u.pendingUpdatePath(), pending); err != nil {
		return Result{}, fmt.Errorf("record downloaded update: %w", err)
	}
	keepWorkRoot = true
	_ = writeReleaseState(plan, releaseUpdateState{TargetVersion: release.Tag, Previous: u.currentVersion, Status: "downloaded", At: downloadedAt})
	status.DownloadReady = true
	status.DownloadedVersion = release.Tag
	status.DownloadedAt = downloadedAt
	status.UpdateAvailable = false
	return Result{
		Status:         status,
		Fetched:        true,
		Updated:        false,
		Forced:         force,
		Downloaded:     true,
		PreviousCommit: u.currentVersion,
		TargetCommit:   release.Tag,
		Output:         fmt.Sprintf("Downloaded and verified %s with SHA-256; %s is ready to install.", u.assetName, release.Tag),
		At:             time.Now(),
	}, nil
}

// InstallDownloaded applies a previously downloaded package and schedules the
// controlled restart with health-check rollback.
func (u *ReleasePackageUpdater) InstallDownloaded(ctx context.Context) (Result, error) {
	status, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if !u.beginOperation() {
		return Result{}, ErrUpdateInProgress
	}
	defer u.endOperation()
	if u.handoffStarted {
		return Result{}, ErrUpdateInProgress
	}
	pending, ok := u.pendingUpdate()
	if !ok {
		return Result{}, errors.New("updater: no downloaded release is ready to install")
	}
	plan, err := readReleaseApplyPlan(pending.PlanPath)
	if err != nil {
		return Result{}, fmt.Errorf("read downloaded release plan: %w", err)
	}
	plan.ParentPID = os.Getpid()
	if err := validateReleaseApplyPlan(plan); err != nil {
		return Result{}, err
	}
	if !regularFileExists(plan.StagedExecutable) || !regularFileExists(filepath.Join(plan.StagedFrontend, "index.html")) {
		return Result{}, fmt.Errorf("%w: downloaded package is incomplete", ErrInvalidReleaseArchive)
	}
	if err := writePrivateJSON(pending.PlanPath, plan); err != nil {
		return Result{}, err
	}
	helperPath := filepath.Join(plan.WorkRoot, "diana-release-helper")
	if runtime.GOOS == "windows" {
		helperPath += ".exe"
	}
	if err := u.startHelper(helperPath, pending.PlanPath, plan.LogPath); err != nil {
		return Result{}, fmt.Errorf("start release update helper: %w", err)
	}
	u.handoffStarted = true
	_ = os.Remove(u.pendingUpdatePath())
	if u.shutdown != nil {
		time.AfterFunc(750*time.Millisecond, u.shutdown)
	}
	status.DownloadReady = false
	status.DownloadedVersion = ""
	status.DownloadedAt = time.Time{}
	status.RestartRequired = true
	return Result{Status: status, Fetched: true, Updated: true, Downloaded: true, Applied: true, RestartRequired: true, PreviousCommit: u.currentVersion, TargetCommit: plan.TargetVersion, Output: fmt.Sprintf("Scheduled installation of %s with automatic restart and health-check rollback.", plan.TargetVersion), At: time.Now()}, nil
}

// Install preserves the original one-call behavior for compatibility.
func (u *ReleasePackageUpdater) Install(ctx context.Context, release ReleasePackage, force bool) (Result, error) {
	if _, err := u.Download(ctx, release, force); err != nil {
		return Result{}, err
	}
	return u.InstallDownloaded(ctx)
}

func (u *ReleasePackageUpdater) pendingUpdatePath() string {
	return filepath.Join(u.installRoot, ".diana-updates", "pending-update.json")
}

func (u *ReleasePackageUpdater) pendingUpdate() (pendingReleaseUpdate, bool) {
	var pending pendingReleaseUpdate
	file, err := os.Open(u.pendingUpdatePath())
	if err != nil {
		return pending, false
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxChecksumBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&pending) != nil || pending.Schema != 1 || !releaseTagPattern.MatchString(pending.TargetVersion) || !filepath.IsAbs(pending.PlanPath) || !pathWithin(filepath.Join(u.installRoot, ".diana-updates"), pending.PlanPath) {
		return pendingReleaseUpdate{}, false
	}
	if _, err := readReleaseApplyPlan(pending.PlanPath); err != nil {
		return pendingReleaseUpdate{}, false
	}
	return pending, true
}

func (u *ReleasePackageUpdater) removePendingUpdate() {
	pending, ok := u.pendingUpdate()
	if ok {
		if plan, err := readReleaseApplyPlan(pending.PlanPath); err == nil && pathWithin(filepath.Join(u.installRoot, ".diana-updates"), plan.WorkRoot) {
			_ = os.RemoveAll(plan.WorkRoot)
		}
	}
	_ = os.Remove(u.pendingUpdatePath())
}

func startDetachedReleaseHelper(executable, planPath, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, InternalReleaseApplyCommand, planPath)
	cmd.Dir = filepath.Dir(executable)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return cmd.Process.Release()
}

func downloadReleaseFile(ctx context.Context, client *http.Client, rawURL, target string, maxBytes int64, progress func(int64, int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "diana-release-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || !validReleaseDownloadURL(resp.Request.URL.String()) {
		return "", errors.New("updater: release asset redirected to an insecure URL")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return "", fmt.Errorf("asset is too large: %d bytes", resp.ContentLength)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	total := resp.ContentLength
	if total <= 0 {
		total = maxBytes
	}
	written, copyErr := io.Copy(io.MultiWriter(file, hash, &progressWriter{total: total, report: progress}), io.LimitReader(resp.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > maxBytes {
		return "", fmt.Errorf("asset exceeds %d bytes", maxBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type progressWriter struct {
	written int64
	total   int64
	report  func(int64, int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	if w.report != nil {
		w.written += int64(len(p))
		w.report(w.written, w.total)
	}
	return len(p), nil
}

func checksumForAsset(manifest []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != assetName {
			continue
		}
		digest := strings.ToLower(fields[0])
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("%w: invalid digest for %s", ErrChecksumMissing, assetName)
		}
		return digest, nil
	}
	return "", fmt.Errorf("%w: %s is not listed", ErrChecksumMissing, assetName)
}

func extractReleaseArchive(archivePath, target string) error {
	if strings.HasSuffix(archivePath, ".tar.gz") {
		return extractTarGZ(archivePath, target)
	}
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZIP(archivePath, target)
	}
	return fmt.Errorf("%w: unsupported archive type", ErrInvalidReleaseArchive)
}

func extractTarGZ(archivePath, target string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReleaseArchive, err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReleaseArchive, err)
		}
		files++
		if files > maxExtractedPackageFiles {
			return fmt.Errorf("%w: too many files", ErrInvalidReleaseArchive)
		}
		destination, err := safeArchiveDestination(target, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxExtractedPackageBytes-total {
				return fmt.Errorf("%w: extracted content is too large", ErrInvalidReleaseArchive)
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o755
			if mode&0o400 == 0 {
				mode |= 0o600
			}
			out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, reader, header.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("%w: archive entry %q has unsupported type", ErrInvalidReleaseArchive, header.Name)
		}
	}
	return nil
}

func extractZIP(archivePath, target string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReleaseArchive, err)
	}
	defer reader.Close()
	if len(reader.File) > maxExtractedPackageFiles {
		return fmt.Errorf("%w: too many files", ErrInvalidReleaseArchive)
	}
	var total uint64
	for _, entry := range reader.File {
		destination, err := safeArchiveDestination(target, entry.Name)
		if err != nil {
			return err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: archive symlink %q is not allowed", ErrInvalidReleaseArchive, entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
			continue
		}
		if entry.UncompressedSize64 > uint64(maxExtractedPackageBytes)-total {
			return fmt.Errorf("%w: extracted content is too large", ErrInvalidReleaseArchive)
		}
		total += entry.UncompressedSize64
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		mode := entry.Mode().Perm() & 0o755
		if mode&0o400 == 0 {
			mode |= 0o600
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		inputCloseErr := input.Close()
		outputCloseErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if outputCloseErr != nil {
			return outputCloseErr
		}
	}
	return nil
}

func safeArchiveDestination(root, name string) (string, error) {
	name = filepath.FromSlash(strings.TrimSpace(name))
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: invalid archive path %q", ErrInvalidReleaseArchive, name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: archive path escapes target: %q", ErrInvalidReleaseArchive, name)
	}
	destination := filepath.Join(root, clean)
	if !pathWithin(root, destination) {
		return "", fmt.Errorf("%w: archive path escapes target: %q", ErrInvalidReleaseArchive, name)
	}
	return destination, nil
}

func releasePackageDirectory(assetName string) string {
	name := strings.TrimSuffix(assetName, ".tar.gz")
	return strings.TrimSuffix(name, ".zip")
}

func validReleaseDownloadURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
