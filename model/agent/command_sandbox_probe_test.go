// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"strings"
	"testing"
)

// 装了不等于能用。bwrap 在 PATH 里、但内核或容器策略禁掉了非特权用户命名空间时，
// 每次调用都会失败——auto 模式承诺的是「没有沙盒就照常执行」，所以探不通必须
// 当作没有，而不是让每条命令都挂掉。
func TestProbeTreatsAnUnusableSandboxAsAbsent(t *testing.T) {
	restoreCandidate, restoreProbe := sandboxCandidateFn, sandboxProbeExec
	t.Cleanup(func() { sandboxCandidateFn, sandboxProbeExec = restoreCandidate, restoreProbe })

	sandboxCandidateFn = func() (commandSandbox, string) {
		return commandSandbox{kind: "bubblewrap", wrap: wrapWithBubblewrap("bwrap")}, ""
	}
	sandboxProbeExec = func(commandSandbox) error {
		return errors.New("bwrap: No permissions to creating new namespace")
	}

	sandbox, reason := probeCommandSandbox()
	if sandbox.available() {
		t.Fatal("an unusable sandbox was reported as available")
	}
	if !strings.Contains(reason, "bubblewrap") || !strings.Contains(reason, "试运行失败") {
		t.Fatalf("reason = %q, want it to name the implementation and the probe failure", reason)
	}
	// 原始错误要留在原因里：区分「没装」和「装了跑不起来」全靠它。
	if !strings.Contains(reason, "No permissions") {
		t.Fatalf("reason dropped the underlying error: %q", reason)
	}
}

// 探测通过时如实报告实现名。
func TestProbeReportsAWorkingSandbox(t *testing.T) {
	restoreCandidate, restoreProbe := sandboxCandidateFn, sandboxProbeExec
	t.Cleanup(func() { sandboxCandidateFn, sandboxProbeExec = restoreCandidate, restoreProbe })

	sandboxCandidateFn = func() (commandSandbox, string) {
		return commandSandbox{kind: "bubblewrap", wrap: wrapWithBubblewrap("bwrap")}, ""
	}
	sandboxProbeExec = func(commandSandbox) error { return nil }

	sandbox, reason := probeCommandSandbox()
	if !sandbox.available() || sandbox.kind != "bubblewrap" {
		t.Fatalf("sandbox = %#v", sandbox)
	}
	if reason != "" {
		t.Fatalf("a working sandbox carried a reason: %q", reason)
	}
}

// Effective 是「这次命令实际会怎么跑」，三种模式各自的落点都要说得准，
// 因为启动日志和控制台都按它显示。
func TestCommandSandboxStatusEffective(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status CommandSandboxStatus
		want   string
	}{
		{"关掉了", CommandSandboxStatus{Mode: CommandSandboxOff}, "unsandboxed"},
		{"auto 有沙盒", CommandSandboxStatus{Mode: CommandSandboxAuto, Available: true}, "sandboxed"},
		{"auto 没沙盒就裸跑", CommandSandboxStatus{Mode: CommandSandboxAuto}, "unsandboxed"},
		{"require 有沙盒", CommandSandboxStatus{Mode: CommandSandboxRequire, Available: true}, "sandboxed"},
		{"require 没沙盒就拒绝", CommandSandboxStatus{Mode: CommandSandboxRequire}, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Effective(); got != tc.want {
				t.Fatalf("Effective() = %q, want %q", got, tc.want)
			}
		})
	}
}

// off 模式不该去碰探测：关掉了就不必解释为什么没有沙盒。
func TestDescribeCommandSandboxOff(t *testing.T) {
	status := DescribeCommandSandbox("OFF")
	if status.Mode != CommandSandboxOff || status.Available || status.Kind != "" {
		t.Fatalf("status = %#v", status)
	}
	if status.Effective() != "unsandboxed" {
		t.Fatalf("Effective() = %q", status.Effective())
	}
}
