// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var errReleaseUpdateRolledBack = errors.New("updater: release update was rolled back")

type releaseApplyFile struct {
	Target string `json:"target"`
	Staged string `json:"staged"`
}

type releaseApplyPlan struct {
	Schema           int      `json:"schema"`
	ParentPID        int      `json:"parent_pid"`
	CurrentVersion   string   `json:"current_version"`
	TargetVersion    string   `json:"target_version"`
	InstallRoot      string   `json:"install_root"`
	WorkRoot         string   `json:"work_root"`
	BackupRoot       string   `json:"backup_root"`
	ExecutablePath   string   `json:"executable_path"`
	StagedExecutable string   `json:"staged_executable"`
	FrontendPath     string   `json:"frontend_path"`
	StagedFrontend   string   `json:"staged_frontend"`
	DatabasePath     string   `json:"database_path"`
	HealthURL        string   `json:"health_url"`
	WorkingDir       string   `json:"working_dir"`
	Arguments        []string `json:"arguments,omitempty"`
	// Supervisor 记录旧进程是否由 launchd/systemd 托管。托管时重启交给管理器，
	// 更新器自己再启动一次就会和管理器抢端口。
	Supervisor    serviceSupervisor  `json:"supervisor,omitempty"`
	OptionalFiles []releaseApplyFile `json:"optional_files,omitempty"`
	LogPath       string             `json:"log_path"`
}

type releaseUpdateState struct {
	TargetVersion  string    `json:"target_version"`
	Previous       string    `json:"previous_version,omitempty"`
	Status         string    `json:"status"`
	BackupRoot     string    `json:"backup_root,omitempty"`
	DatabaseBackup string    `json:"database_backup,omitempty"`
	Error          string    `json:"error,omitempty"`
	At             time.Time `json:"at"`
}

type releaseManagedProcess interface {
	Stop() error
	Release() error
}

type commandManagedProcess struct {
	cmd *exec.Cmd
}

func (p *commandManagedProcess) Stop() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	killErr := p.cmd.Process.Kill()
	waitErr := p.cmd.Wait()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return killErr
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return waitErr
		}
	}
	return nil
}

func (p *commandManagedProcess) Release() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Release()
}

type releaseApplyHooks struct {
	waitForParent  func(pid int, timeout time.Duration) error
	launch         func(releaseApplyPlan) (releaseManagedProcess, error)
	health         func(context.Context, string) error
	restartService func(serviceSupervisor) error
}

func defaultReleaseApplyHooks() releaseApplyHooks {
	return releaseApplyHooks{
		waitForParent:  waitForProcessExit,
		launch:         launchReleaseProcess,
		health:         waitForReleaseHealth,
		restartService: restartSupervisedService,
	}
}

// startReleaseInstance 把「让服务重新跑起来」这件事收敛到一个地方。
//
// 被 launchd/systemd 托管时，管理器已经在旧进程退出的瞬间把服务拉起来了
// （那时文件还没换，跑的仍是旧版本）。这里换完文件后请管理器再重启一次，
// 由它串行地停旧起新；更新器自己绝不能再 exec 一个实例出来，否则两边
// 抢 127.0.0.1 的监听端口，后启动的直接 address already in use 退出。
func startReleaseInstance(plan releaseApplyPlan, hooks releaseApplyHooks) (releaseManagedProcess, error) {
	if !plan.Supervisor.Managed() {
		return hooks.launch(plan)
	}
	if hooks.restartService == nil {
		return nil, errors.New("updater: missing service restart hook")
	}
	if err := hooks.restartService(plan.Supervisor); err != nil {
		return nil, fmt.Errorf("restart %s service: %w", plan.Supervisor, err)
	}
	return supervisedProcess{}, nil
}

// RunReleaseApplyHelper executes the internal post-shutdown update handoff.
// It is called by cmd/webui before normal application initialization.
func RunReleaseApplyHelper(planPath string) error {
	plan, err := readReleaseApplyPlan(planPath)
	if err != nil {
		return err
	}
	if err := validateReleaseApplyPlan(plan); err != nil {
		return err
	}
	if err := applyReleasePlan(plan, defaultReleaseApplyHooks()); err != nil {
		if !errors.Is(err, errReleaseUpdateRolledBack) {
			_ = writeReleaseState(plan, releaseUpdateState{
				TargetVersion: plan.TargetVersion,
				Previous:      plan.CurrentVersion,
				Status:        "failed",
				BackupRoot:    plan.BackupRoot,
				Error:         err.Error(),
				At:            time.Now(),
			})
		}
		return err
	}
	return nil
}

func readReleaseApplyPlan(path string) (releaseApplyPlan, error) {
	var plan releaseApplyPlan
	file, err := os.Open(path)
	if err != nil {
		return plan, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxChecksumBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, fmt.Errorf("decode release update plan: %w", err)
	}
	return plan, nil
}

func validateReleaseApplyPlan(plan releaseApplyPlan) error {
	if plan.Schema != 1 || plan.ParentPID <= 0 {
		return errors.New("updater: invalid release update plan header")
	}
	for name, path := range map[string]string{
		"install root":      plan.InstallRoot,
		"work root":         plan.WorkRoot,
		"backup root":       plan.BackupRoot,
		"executable":        plan.ExecutablePath,
		"staged executable": plan.StagedExecutable,
		"frontend":          plan.FrontendPath,
		"staged frontend":   plan.StagedFrontend,
		"database":          plan.DatabasePath,
		"working directory": plan.WorkingDir,
		"log":               plan.LogPath,
	} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("updater: %s path is not absolute", name)
		}
	}
	updatesRoot := filepath.Join(plan.InstallRoot, ".diana-updates")
	if !pathWithin(updatesRoot, plan.WorkRoot) || !pathWithin(filepath.Join(updatesRoot, "backups"), plan.BackupRoot) {
		return errors.New("updater: update workspace escapes the install root")
	}
	if !pathWithin(plan.InstallRoot, plan.ExecutablePath) || !pathWithin(plan.InstallRoot, plan.FrontendPath) {
		return errors.New("updater: package target escapes the install root")
	}
	if !pathWithin(plan.WorkRoot, plan.StagedExecutable) || !pathWithin(plan.WorkRoot, plan.StagedFrontend) {
		return errors.New("updater: staged package escapes the update workspace")
	}
	if !validReleaseHealthURL(plan.HealthURL) {
		return errors.New("updater: health-check URL must target loopback HTTP")
	}
	for _, file := range plan.OptionalFiles {
		if !pathWithin(plan.InstallRoot, file.Target) || !pathWithin(plan.WorkRoot, file.Staged) {
			return errors.New("updater: optional package file escapes an allowed root")
		}
	}
	if err := validateServiceSupervisor(plan.Supervisor); err != nil {
		return err
	}
	return nil
}

func applyReleasePlan(plan releaseApplyPlan, hooks releaseApplyHooks) error {
	if hooks.waitForParent == nil || hooks.launch == nil || hooks.health == nil {
		return errors.New("updater: incomplete release apply hooks")
	}
	if err := hooks.waitForParent(plan.ParentPID, 45*time.Second); err != nil {
		return fmt.Errorf("wait for old Diana process: %w", err)
	}
	if err := os.MkdirAll(plan.BackupRoot, 0o700); err != nil {
		return restartPreviousRelease(plan, hooks, fmt.Errorf("create update backup: %w", err), "")
	}
	if err := writePrivateJSON(filepath.Join(plan.BackupRoot, "apply-plan.json"), plan); err != nil {
		return restartPreviousRelease(plan, hooks, fmt.Errorf("record update backup plan: %w", err), "")
	}
	databaseBackup, err := backupReleaseDatabase(plan.DatabasePath, plan.BackupRoot)
	if err != nil {
		return restartPreviousRelease(plan, hooks, fmt.Errorf("backup SQLite database: %w", err), "")
	}

	files := []releaseApplyFile{
		{Target: plan.ExecutablePath, Staged: plan.StagedExecutable},
		{Target: plan.FrontendPath, Staged: plan.StagedFrontend},
	}
	files = append(files, plan.OptionalFiles...)
	switched, err := switchReleaseFiles(plan.InstallRoot, plan.BackupRoot, files)
	if err != nil {
		return rollbackReleasePlan(plan, hooks, switched, databaseBackup, fmt.Errorf("switch release files: %w", err))
	}
	// 新二进制刚落地，签名还是 Release 里的样子（CI 产物根本没签）。先按固定
	// identifier 重签再启动，否则 macOS 会把它当成一个全新程序重新登记，隐私
	// 列表里多出一条同名 Diana，之前给过的授权也全部作废。签不上不算更新失败：
	// 顶多是授权要重点一次，回滚反而让用户彻底升不上去。
	if err := restoreMacOSIdentity(plan.ExecutablePath); err != nil {
		log.Printf("updater: restore macOS code identity: %v", err)
	}

	process, err := startReleaseInstance(plan, hooks)
	if err != nil {
		return rollbackReleasePlan(plan, hooks, switched, databaseBackup, fmt.Errorf("start updated Diana: %w", err))
	}
	healthCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	healthErr := hooks.health(healthCtx, plan.HealthURL)
	cancel()
	if healthErr == nil {
		if err := process.Release(); err != nil {
			return fmt.Errorf("release updated process: %w", err)
		}
		_ = writeReleaseState(plan, releaseUpdateState{
			TargetVersion:  plan.TargetVersion,
			Previous:       plan.CurrentVersion,
			Status:         "healthy",
			BackupRoot:     plan.BackupRoot,
			DatabaseBackup: databaseBackup,
			At:             time.Now(),
		})
		pruneReleaseBackups(filepath.Dir(plan.BackupRoot), 3)
		_ = os.RemoveAll(plan.WorkRoot)
		return nil
	}

	rollbackReason := fmt.Errorf("updated Diana failed health check: %w", healthErr)
	if stopErr := process.Stop(); stopErr != nil {
		rollbackReason = fmt.Errorf("%v; stop updated process: %w", rollbackReason, stopErr)
	}
	return rollbackReleasePlan(plan, hooks, switched, databaseBackup, rollbackReason)
}

func rollbackReleasePlan(plan releaseApplyPlan, hooks releaseApplyHooks, switched []switchedReleaseFile, databaseBackup string, reason error) error {
	rollbackErr := restoreReleaseFiles(switched)
	databaseErr := restoreReleaseDatabase(plan.DatabasePath, databaseBackup)
	if rollbackErr != nil || databaseErr != nil {
		return fmt.Errorf("%v; rollback files: %v; rollback database: %v", reason, rollbackErr, databaseErr)
	}
	return restartPreviousRelease(plan, hooks, reason, databaseBackup)
}

func restartPreviousRelease(plan releaseApplyPlan, hooks releaseApplyHooks, reason error, databaseBackup string) error {
	oldProcess, launchErr := startReleaseInstance(plan, hooks)
	if launchErr != nil {
		return fmt.Errorf("%v; old version restore launch failed: %w", reason, launchErr)
	}
	rollbackHealthCtx, rollbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
	rollbackHealthErr := hooks.health(rollbackHealthCtx, plan.HealthURL)
	rollbackCancel()
	if rollbackHealthErr != nil {
		_ = oldProcess.Stop()
		return fmt.Errorf("%v; restored version also failed health check: %w", reason, rollbackHealthErr)
	}
	if err := oldProcess.Release(); err != nil {
		return err
	}
	_ = writeReleaseState(plan, releaseUpdateState{
		TargetVersion:  plan.TargetVersion,
		Previous:       plan.CurrentVersion,
		Status:         "rolled_back",
		BackupRoot:     plan.BackupRoot,
		DatabaseBackup: databaseBackup,
		Error:          reason.Error(),
		At:             time.Now(),
	})
	_ = os.RemoveAll(plan.WorkRoot)
	return fmt.Errorf("%w: %v", errReleaseUpdateRolledBack, reason)
}

type switchedReleaseFile struct {
	target      string
	backup      string
	hadPrevious bool
}

func switchReleaseFiles(installRoot, backupRoot string, files []releaseApplyFile) ([]switchedReleaseFile, error) {
	switched := make([]switchedReleaseFile, 0, len(files))
	for _, file := range files {
		if _, err := os.Stat(file.Staged); err != nil {
			return switched, err
		}
		relative, err := filepath.Rel(installRoot, file.Target)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return switched, errors.New("updater: invalid package target")
		}
		backup := filepath.Join(backupRoot, "package", relative)
		if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
			return switched, err
		}
		entry := switchedReleaseFile{target: file.Target, backup: backup}
		if _, err := os.Stat(file.Target); err == nil {
			if err := os.Rename(file.Target, backup); err != nil {
				return switched, err
			}
			entry.hadPrevious = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return switched, err
		}
		switched = append(switched, entry)
		if err := os.MkdirAll(filepath.Dir(file.Target), 0o755); err != nil {
			return switched, err
		}
		if err := os.Rename(file.Staged, file.Target); err != nil {
			return switched, err
		}
	}
	return switched, nil
}

func restoreReleaseFiles(switched []switchedReleaseFile) error {
	var failures []string
	for index := len(switched) - 1; index >= 0; index-- {
		entry := switched[index]
		if err := os.RemoveAll(entry.target); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if entry.hadPrevious {
			if err := os.MkdirAll(filepath.Dir(entry.target), 0o755); err != nil {
				failures = append(failures, err.Error())
				continue
			}
			if err := os.Rename(entry.backup, entry.target); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func backupReleaseDatabase(databasePath, backupRoot string) (string, error) {
	backupDir := filepath.Join(backupRoot, "database")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	mainBackup := ""
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := databasePath + suffix
		if !regularFileExists(source) {
			continue
		}
		target := filepath.Join(backupDir, filepath.Base(databasePath)+suffix)
		if err := copyRegularFile(source, target, 0); err != nil {
			return "", err
		}
		if suffix == "" {
			mainBackup = target
		}
	}
	if mainBackup == "" {
		return "", fmt.Errorf("database %s does not exist", databasePath)
	}
	return mainBackup, nil
}

func restoreReleaseDatabase(databasePath, mainBackup string) error {
	if strings.TrimSpace(mainBackup) == "" {
		return errors.New("updater: database backup path is empty")
	}
	backupDir := filepath.Dir(mainBackup)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(databasePath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		backup := filepath.Join(backupDir, filepath.Base(databasePath)+suffix)
		if regularFileExists(backup) {
			if err := copyRegularFile(backup, databasePath+suffix, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyRegularFile(source, target string, forcedMode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("updater: %s is not a regular file", source)
	}
	mode := info.Mode().Perm()
	if forcedMode != 0 {
		mode = forcedMode
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".diana-copy-*")
	if err != nil {
		return err
	}
	temporaryPath := output.Name()
	defer os.Remove(temporaryPath)
	if err := output.Chmod(mode); err != nil {
		_ = output.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, target)
}

func launchReleaseProcess(plan releaseApplyPlan) (releaseManagedProcess, error) {
	logFile, err := os.OpenFile(plan.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(plan.ExecutablePath, plan.Arguments...)
	cmd.Dir = plan.WorkingDir
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = logFile.Close()
	return &commandManagedProcess{cmd: cmd}, nil
}

func waitForReleaseHealth(ctx context.Context, healthURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			var payload struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && payload.Status == "ok" {
				return nil
			}
			lastErr = fmt.Errorf("health HTTP %d", response.StatusCode)
			if decodeErr != nil {
				lastErr = decodeErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return lastErr
		case <-ticker.C:
		}
	}
}

func validReleaseHealthURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func writeReleaseState(plan releaseApplyPlan, state releaseUpdateState) error {
	return writePrivateJSON(filepath.Join(plan.InstallRoot, ".diana-updates", "last-update.json"), state)
}

func readReleaseState(installRoot string) (releaseUpdateState, bool) {
	var state releaseUpdateState
	file, err := os.Open(filepath.Join(installRoot, ".diana-updates", "last-update.json"))
	if err != nil {
		return state, false
	}
	defer file.Close()
	if err := json.NewDecoder(io.LimitReader(file, maxChecksumBytes)).Decode(&state); err != nil {
		return releaseUpdateState{}, false
	}
	return state, true
}

func pruneReleaseBackups(backupsRoot string, keep int) {
	entries, err := os.ReadDir(backupsRoot)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.RemoveAll(filepath.Join(backupsRoot, names[0]))
		names = names[1:]
	}
}
