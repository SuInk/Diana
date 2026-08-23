// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 固定 identifier 是这套方案的地基：它必须和 Info.plist 的 CFBundleIdentifier
// 以及两个脚本里的签名标识一致，写歪一处 macOS 就当成另一个程序重新登记。
func TestMacOSCodeIdentifierMatchesPackaging(t *testing.T) {
	if MacOSCodeIdentifier != "com.suink.diana" {
		t.Fatalf("identifier = %q", MacOSCodeIdentifier)
	}
	plist, err := os.ReadFile(filepath.Join("..", "..", "packaging", "macos", "Info.plist"))
	if err != nil {
		t.Fatalf("read Info.plist: %v", err)
	}
	if !strings.Contains(string(plist), "<string>"+MacOSCodeIdentifier+"</string>") {
		t.Fatalf("Info.plist does not declare %q", MacOSCodeIdentifier)
	}
	for _, script := range []string{"build-local-mac.sh", "install.sh"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "scripts", script))
		if err != nil {
			t.Fatalf("read %s: %v", script, err)
		}
		if !strings.Contains(string(content), MacOSCodeIdentifier) {
			t.Fatalf("%s does not use %q", script, MacOSCodeIdentifier)
		}
		// 两个脚本都必须改写 designated requirement。漏掉它，ad-hoc 签名就会把
		// cdhash 钉进去，下次更新换了二进制授权立刻作废。
		if !strings.Contains(string(content), "=designated => identifier") {
			t.Fatalf("%s signs without an identifier-only designated requirement", script)
		}
	}
}

// 安装脚本把运行时装进 .app 时，前端必须一起放进 bundle：Release 自更新器要求
// 前端目录在可执行文件所在目录之内，否则会判定为「不支持自更新」。
func TestInstallScriptKeepsFrontendInsideBundle(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	script := string(content)
	for _, want := range []string{
		`macos_app_dir="$install_dir/Diana.app"`,
		`macos_app_binary="$macos_app_dir/Contents/MacOS/$binary_name"`,
		`cp -R "$install_dir/frontend-next" "$macos_app_dir/Contents/MacOS/frontend-next"`,
		`frontend_dist_q=$(shell_quote "$macos_app_dir/Contents/MacOS/frontend-next/dist")`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh is missing %q", want)
		}
	}
}

// designated requirement 只能要求 identifier。带上 cdhash 就等于钉死当前二进制，
// 下个版本立刻不满足，授权作废——这正是要避免的那件事。
func TestMacOSDesignatedRequirementOmitsCodeHash(t *testing.T) {
	requirement := macOSDesignatedRequirement(MacOSCodeIdentifier)
	if requirement != `=designated => identifier "com.suink.diana"` {
		t.Fatalf("requirement = %q", requirement)
	}
	if strings.Contains(requirement, "cdhash") {
		t.Fatalf("requirement must not pin a code hash: %q", requirement)
	}
}

// 装在 .app 里时要重签整个 bundle，只签里面的可执行文件会让 bundle 签名失效。
func TestEnclosingMacOSAppBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle layout detection only applies to macOS")
	}
	root := t.TempDir()
	bundled := filepath.Join(root, "Diana.app", "Contents", "MacOS", "diana-webui")
	if got := enclosingMacOSAppBundle(bundled); got != filepath.Join(root, "Diana.app") {
		t.Fatalf("bundle = %q", got)
	}
	for _, bare := range []string{
		filepath.Join(root, "diana-webui"),
		filepath.Join(root, "Diana.app", "Contents", "Helpers", "diana-webui"),
		filepath.Join(root, "Diana", "Contents", "MacOS", "diana-webui"),
		"",
	} {
		if got := enclosingMacOSAppBundle(bare); got != "" {
			t.Fatalf("enclosingMacOSAppBundle(%q) = %q, want empty", bare, got)
		}
	}
}

// 非 macOS 平台上这套机制不存在，所有入口都必须是安静的空操作。
func TestMacOSIdentityHelpersAreNoopsOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this guards the non-macOS behaviour")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "diana-webui")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	if err := resignMacOSPath(executable); err != nil {
		t.Fatalf("resignMacOSPath() = %v", err)
	}
	if err := restoreMacOSIdentity(executable); err != nil {
		t.Fatalf("restoreMacOSIdentity() = %v", err)
	}
	if got := enclosingMacOSAppBundle(filepath.Join(root, "Diana.app", "Contents", "MacOS", "diana-webui")); got != "" {
		t.Fatalf("bundle detection should stay inert off macOS, got %q", got)
	}
}

// 路径不存在时要如实报错，不能假装签过了。
func TestResignMacOSPathReportsMissingTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("resign only runs on macOS")
	}
	if err := resignMacOSPath(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if err := resignMacOSPath("   "); err != nil {
		t.Fatalf("blank path should be ignored: %v", err)
	}
}
