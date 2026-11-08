// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// renameFixture 是一份一键安装布局：程序叫 diana-webui，start-installed.sh 点名它，
// 这次更新要把程序改名为 Diana。
func renameFixture(t *testing.T) releaseApplyPlan {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix layout")
	}
	plan := releaseApplyFixture(t)
	old := filepath.Join(plan.InstallRoot, "diana-webui")
	if err := os.Rename(plan.ExecutablePath, old); err != nil {
		t.Fatal(err)
	}
	plan.ExecutablePath = old
	writeUpdaterTestFile(t, filepath.Join(plan.InstallRoot, "start-installed.sh"), "#!/bin/sh\nexec \"$root/diana-webui\" --config x\n", 0o755)
	launchers, err := planRenameLaunchers(old)
	if err != nil {
		t.Fatal(err)
	}
	plan.RenamedExecutable = filepath.Join(plan.InstallRoot, "Diana")
	plan.Launchers = launchers
	if err := validateReleaseApplyPlan(plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func renameHooks(t *testing.T, healthy func(launched string) error) releaseApplyHooks {
	var launched []string
	return releaseApplyHooks{
		waitForParent: func(int, time.Duration) error { return nil },
		launch: func(p releaseApplyPlan) (releaseManagedProcess, error) {
			launched = append(launched, p.ExecutablePath)
			return &fakeReleaseProcess{}, nil
		},
		health: func(context.Context, string) error { return healthy(launched[len(launched)-1]) },
	}
}

func TestApplyReleasePlanRenamesProgramAndKeepsOldEntryWorking(t *testing.T) {
	plan := renameFixture(t)
	launcher := filepath.Join(plan.InstallRoot, "start-installed.sh")
	if len(plan.Launchers) != 1 || plan.Launchers[0] != launcher {
		t.Fatalf("launchers = %v", plan.Launchers)
	}
	err := applyReleasePlan(plan, renameHooks(t, func(launched string) error {
		if launched != plan.RenamedExecutable {
			t.Errorf("started %s, want the renamed program", launched)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertUpdaterTestContent(t, plan.RenamedExecutable, "new-binary")
	// 旧名字成了指向新程序的软链接，手写的启动脚本和 PATH 里的 diana 命令照常能用。
	if target, err := os.Readlink(plan.ExecutablePath); err != nil || target != "Diana" {
		t.Fatalf("old entry = %q, %v", target, err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "new-binary")
	assertUpdaterTestContent(t, launcher, "#!/bin/sh\nexec \"$root/Diana\" --config x\n")
	// 之后从旧入口启动的进程，更新器按真实文件认出自己是 Diana。
	u, err := NewReleasePackageUpdater(ReleasePackageOptions{Executable: plan.ExecutablePath, PlatformOS: "linux", PlatformArch: "amd64", WorkingDir: plan.InstallRoot})
	if err != nil || filepath.Base(u.executable) != "Diana" {
		t.Fatalf("updater executable = %v, %v", u, err)
	}
}

func TestApplyReleasePlanRollsBackRename(t *testing.T) {
	plan := renameFixture(t)
	launcher := filepath.Join(plan.InstallRoot, "start-installed.sh")
	health := 0
	err := applyReleasePlan(plan, renameHooks(t, func(launched string) error {
		health++
		if health == 1 {
			return errors.New("new version unhealthy")
		}
		if launched != plan.ExecutablePath {
			t.Errorf("rollback started %s, want the old program", launched)
		}
		return nil
	}))
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("applyReleasePlan() error = %v", err)
	}
	if info, err := os.Lstat(plan.ExecutablePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("old program not restored as a regular file: %v %v", info, err)
	}
	assertUpdaterTestContent(t, plan.ExecutablePath, "old-binary")
	if _, err := os.Lstat(plan.RenamedExecutable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed program left behind: %v", err)
	}
	assertUpdaterTestContent(t, launcher, "#!/bin/sh\nexec \"$root/diana-webui\" --config x\n")
}

// .app 里不留软链接（会破坏整包签名），改 Info.plist 和 bundle 旁边的
// start-installed.sh；找不到 start-installed.sh 就不改名，免得 launchd 拉一个不存在的文件。
func TestRenameInsideMacOSAppBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle layout")
	}
	plan := releaseApplyFixture(t)
	bundle := filepath.Join(plan.InstallRoot, "Diana.app")
	old := filepath.Join(bundle, "Contents", "MacOS", "diana-webui")
	writeUpdaterTestFile(t, old, "old-binary", 0o700)
	if _, err := planRenameLaunchers(old); err == nil || !strings.Contains(err.Error(), "one-click installer") {
		t.Fatalf("bundle without launcher accepted: %v", err)
	}
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	writeUpdaterTestFile(t, plist, "<key>CFBundleExecutable</key><string>diana-webui</string>", 0o644)
	starter := filepath.Join(plan.InstallRoot, "start-installed.sh")
	writeUpdaterTestFile(t, starter, "app_binary=\"$app_dir/Contents/MacOS/diana-webui\"\n", 0o755)
	launchers, err := planRenameLaunchers(old)
	if err != nil || len(launchers) != 2 {
		t.Fatalf("launchers = %v, %v", launchers, err)
	}
	plan.UpdatesRoot = filepath.Join(plan.InstallRoot, ".diana-updates")
	plan.InstallRoot = filepath.Dir(old)
	plan.ExecutablePath = old
	plan.RenamedExecutable = filepath.Join(filepath.Dir(old), "Diana")
	plan.Launchers = launchers
	if err := validateReleaseApplyPlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := applyReleasePlan(plan, renameHooks(t, func(string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle kept an entry under the old name: %v", err)
	}
	assertUpdaterTestContent(t, plan.RenamedExecutable, "new-binary")
	assertUpdaterTestContent(t, plist, "<key>CFBundleExecutable</key><string>Diana</string>")
	assertUpdaterTestContent(t, starter, "app_binary=\"$app_dir/Contents/MacOS/Diana\"\n")
}

func TestValidateRenamePlanRejectsUnknownTargets(t *testing.T) {
	plan := renameFixture(t)
	elsewhere := plan
	elsewhere.RenamedExecutable = filepath.Join(plan.WorkRoot, "Diana")
	if validateReleaseApplyPlan(elsewhere) == nil {
		t.Fatal("renamed program outside the install directory accepted")
	}
	stray := plan
	stray.Launchers = []string{filepath.Join(plan.InstallRoot, "config.yaml")}
	if validateReleaseApplyPlan(stray) == nil {
		t.Fatal("rewriting an arbitrary file accepted")
	}
	noRename := plan
	noRename.RenamedExecutable = ""
	if validateReleaseApplyPlan(noRename) == nil {
		t.Fatal("launcher rewrite without a rename accepted")
	}
	if validInstallName("linux", "Diana.exe") || validInstallName("windows", "Diana") || !validInstallName("windows", "Diana.exe") || validInstallName("linux", "../Diana") {
		t.Fatal("install_as name rules")
	}
}
