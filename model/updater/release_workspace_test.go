// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 容器里 /app 对运行用户只读，往安装目录建工作目录会直接 permission denied。
// 数据目录是挂进来的可写卷，更新就该落在那儿。
func TestUpdatesDirFollowsDataDirectory(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "app")
	dataDir := filepath.Join(root, "data")
	for _, dir := range []string{installRoot, dataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(dataDir, "diana.db")
	if err := os.WriteFile(databasePath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(installRoot, expectedReleaseBinaryName(runtime.GOOS, runtime.GOARCH))

	u, err := NewReleasePackageUpdater(ReleasePackageOptions{
		Executable:   executable,
		DatabasePath: databasePath,
		WorkingDir:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.updatesDir(), filepath.Join(dataDir, ".diana-updates"); got != want {
		t.Fatalf("更新工作目录 = %q，应当跟着数据目录走（%q）", got, want)
	}

	custom := filepath.Join(root, "writable")
	u, err = NewReleasePackageUpdater(ReleasePackageOptions{
		Executable:   executable,
		DatabasePath: databasePath,
		WorkingDir:   root,
		UpdatesDir:   custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.updatesDir() != custom {
		t.Fatalf("配置指定的工作目录 = %q，应当是 %q", u.updatesDir(), custom)
	}

	// 没有数据库的部署本来就不支持包替换，保持老路径不变。
	u, err = NewReleasePackageUpdater(ReleasePackageOptions{Executable: executable, WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.updatesDir(), filepath.Join(installRoot, ".diana-updates"); got != want {
		t.Fatalf("回落工作目录 = %q，应当是 %q", got, want)
	}
}

// 上一次更新被打断会在工作目录里留下半个暂存目录，下一次更新开始前清掉。
func TestDownloadClearsInterruptedStageDirs(t *testing.T) {
	updatesRoot := t.TempDir()
	stale := filepath.Join(updatesRoot, "stage-123")
	if err := os.MkdirAll(filepath.Join(stale, "package"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(updatesRoot, "backups")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}

	removeStaleStageDirs(updatesRoot)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("中断残留的暂存目录还在：%v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("备份目录被误删：%v", err)
	}
}

// 旧版本写下的计划里没有 updates_root，助手得按老路径找工作目录，
// 否则更新装到一半升级上来的实例会把校验做在错误的根上。
func TestApplyPlanUpdatesDirFallsBackToInstallRoot(t *testing.T) {
	plan := releaseApplyPlan{InstallRoot: filepath.FromSlash("/opt/diana")}
	if got, want := plan.updatesDir(), filepath.Join("/opt/diana", ".diana-updates"); got != want {
		t.Fatalf("旧计划工作目录 = %q，应当是 %q", got, want)
	}
	plan.UpdatesRoot = filepath.FromSlash("/data/.diana-updates")
	if plan.updatesDir() != plan.UpdatesRoot {
		t.Fatalf("新计划工作目录 = %q，应当是 %q", plan.updatesDir(), plan.UpdatesRoot)
	}
}
