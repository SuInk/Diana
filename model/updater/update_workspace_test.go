// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 容器部署里可执行文件在 /app 下，运行用户对它没有写权限；data 目录才是挂进来
// 可写的那个。更新工作目录必须跟着 data 走，否则自更新在 mkdir 就炸。
func TestUpdatesRootFollowsDatabaseDirectory(t *testing.T) {
	dataDir := t.TempDir()
	installRoot := t.TempDir()
	root, err := resolveUpdatesRoot("", filepath.Join(dataDir, "diana.db"), installRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, ".diana-updates"); root != want {
		t.Fatalf("updates root = %q, want %q", root, want)
	}
}

// 显式配置优先于 data 目录：只读根文件系统的部署要能把暂存放到别处。
func TestUpdatesRootPrefersExplicitWorkspace(t *testing.T) {
	workspace := t.TempDir()
	root, err := resolveUpdatesRoot(workspace, filepath.Join(t.TempDir(), "diana.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if root != workspace {
		t.Fatalf("updates root = %q, want %q", root, workspace)
	}
}

// 既没配置也没有数据库路径时保持老行为，别把还没初始化的进程带到奇怪的地方。
func TestUpdatesRootFallsBackToInstallRoot(t *testing.T) {
	installRoot := t.TempDir()
	root, err := resolveUpdatesRoot("", "", installRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(installRoot, ".diana-updates"); root != want {
		t.Fatalf("updates root = %q, want %q", root, want)
	}
}

// 助手进程只认计划文件里的工作目录，不再自己从 InstallRoot 推算。
func TestPlanUpdatesRootUsesPlanValue(t *testing.T) {
	plan := releaseApplyPlan{InstallRoot: "/app", UpdatesRoot: "/data/.diana-updates"}
	if got := planUpdatesRoot(plan); got != "/data/.diana-updates" {
		t.Fatalf("plan updates root = %q", got)
	}
	// 旧版本写下的计划没有这一项，按老规矩回落，正在进行的升级不会半路断掉。
	legacy := releaseApplyPlan{InstallRoot: "/app"}
	if got := planUpdatesRoot(legacy); got != filepath.Join("/app", ".diana-updates") {
		t.Fatalf("legacy plan updates root = %q", got)
	}
}

// 升级前在老位置下载好的那一份仍然认得，不必重下一遍。
func TestPendingUpdateFallsBackToLegacyWorkspace(t *testing.T) {
	installRoot := t.TempDir()
	dataDir := t.TempDir()
	updater := &ReleasePackageUpdater{installRoot: installRoot, updatesRoot: filepath.Join(dataDir, ".diana-updates")}

	legacyRoot := filepath.Join(installRoot, ".diana-updates")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(legacyRoot, "plan.json")
	plan := releaseApplyPlan{
		Schema: 1, CurrentVersion: "v0.9.0", TargetVersion: "v1.0.0",
		InstallRoot: installRoot, UpdatesRoot: legacyRoot,
		WorkRoot: filepath.Join(legacyRoot, "stage-1"), BackupRoot: filepath.Join(legacyRoot, "backups", "b1"),
		ExecutablePath: filepath.Join(installRoot, "diana-webui"), StagedExecutable: filepath.Join(legacyRoot, "stage-1", "diana-webui"),
		FrontendPath: filepath.Join(installRoot, "frontend-next", "dist"), StagedFrontend: filepath.Join(legacyRoot, "stage-1", "dist"),
		DatabasePath: filepath.Join(dataDir, "diana.db"), HealthURL: "http://127.0.0.1:18080/api/health",
		WorkingDir: installRoot, LogPath: filepath.Join(legacyRoot, "last-update.log"),
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pending := pendingReleaseUpdate{Schema: 1, TargetVersion: "v1.0.0", PlanPath: planPath}
	raw, err = json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "pending-update.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := updater.pendingUpdate()
	if !ok || got.TargetVersion != "v1.0.0" || got.PlanPath != planPath {
		t.Fatalf("pending = %#v ok=%v", got, ok)
	}
}
