// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"github.com/SuInk/diana/model/agent"
	"strings"
	"testing"
)

// 不等确认，但先提醒再装：提醒必须早于安装，且写明装了哪些包。
func TestInstallPackageNotifiesBeforeInstalling(t *testing.T) {
	var order []string
	var notice string
	tool := &dianaInstallPackageTool{root: t.TempDir()}
	tool.notify = func(_ context.Context, text string) bool {
		order = append(order, "notify")
		notice = text
		return true
	}
	tool.install = func(_ context.Context, _ agent.Config, packages []string) (string, error) {
		order = append(order, "install:"+strings.Join(packages, ","))
		return "added 2 packages", nil
	}
	out, err := tool.Run(context.Background(), map[string]any{"packages": []any{"sharp", "lodash@4", "sharp"}, "reason": "批量压图"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "|") != "notify|install:sharp,lodash@4" {
		t.Fatalf("顺序 = %v", order)
	}
	for _, want := range []string{"sharp", "lodash@4", "批量压图"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("提醒缺 %q: %s", want, notice)
		}
	}
	if !strings.Contains(out, `"status":"installed"`) || !strings.Contains(out, "node_modules") {
		t.Fatalf("结果 = %s", out)
	}
}

// 包名不合法时一个都不装，也不发提醒。
func TestInstallPackageRejectsUnsafeSpecs(t *testing.T) {
	for _, spec := range []string{"--global", "-g", "https://evil.example/x.tgz", "git+ssh://x", "../x", "file:../x", "a b", "github:u/r", "/abs"} {
		called := false
		tool := &dianaInstallPackageTool{root: t.TempDir()}
		tool.notify = func(context.Context, string) bool { called = true; return true }
		tool.install = func(context.Context, agent.Config, []string) (string, error) { called = true; return "", nil }
		if _, err := tool.Run(context.Background(), map[string]any{"packages": []any{spec}}); err == nil {
			t.Fatalf("%q 应被拒绝", spec)
		}
		if called {
			t.Fatalf("%q 被拒绝前已经提醒或安装", spec)
		}
	}
}

func TestInstallPackageReportsFailure(t *testing.T) {
	tool := &dianaInstallPackageTool{root: t.TempDir()}
	tool.notify = func(context.Context, string) bool { return true }
	tool.install = func(context.Context, agent.Config, []string) (string, error) {
		return "E404 not found", errors.New("npm install 失败")
	}
	out, err := tool.Run(context.Background(), map[string]any{"packages": []any{"no-such-pkg"}})
	if err == nil || !strings.Contains(out, "E404") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// 只给开了文件写入的主人；群友、安全模式都拿不到。
func TestInstallPackageRegisteredForOwnerOnly(t *testing.T) {
	standard := safeModeTestRegistry(t, AgentModeStandard)
	if _, ok := standard.Get(dianaInstallPackageToolName); !ok {
		t.Fatal("标准模式主人没有 install_package")
	}
	safe := safeModeTestRegistry(t, AgentModeSafe)
	if _, ok := safe.Get(dianaInstallPackageToolName); ok {
		t.Fatal("安全模式仍可装包")
	}
}
