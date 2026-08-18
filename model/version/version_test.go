// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package version

import (
	"strings"
	"testing"
)

// TestSourceIsSemantic 保证 VERSION 文件始终维持可比较的语义化版本号。
func TestSourceIsSemantic(t *testing.T) {
	if !IsSemantic(Source()) {
		t.Fatalf("VERSION must hold a semantic version such as v0.8.36, got %q", Source())
	}
}

// TestResolveKeepsInjectedRelease 验证 Release 构建注入的 tag 原样保留。
func TestResolveKeepsInjectedRelease(t *testing.T) {
	if got := Resolve("v1.2.3"); got != "v1.2.3" {
		t.Fatalf("injected release version = %q", got)
	}
	if got := Resolve(" v1.2.3+4 "); got != "v1.2.3+4" {
		t.Fatalf("injected development tag = %q", got)
	}
}

// TestResolveFallsBackToSource 验证未注入版本号时回落到源码基线，而不是无法比较的 dev。
func TestResolveFallsBackToSource(t *testing.T) {
	source := Source()
	if got := Resolve(""); got != source+"-dev" {
		t.Fatalf("empty build version = %q", got)
	}
	if got := Resolve("dev"); got != source+"-dev" {
		t.Fatalf("dev build version = %q", got)
	}
	got := Resolve("038932f")
	if !strings.HasPrefix(got, source+"-dev+") || !strings.HasSuffix(got, "038932f") {
		t.Fatalf("commit build version = %q", got)
	}
}

// TestIsSemantic 覆盖版本号格式判断的边界。
func TestIsSemantic(t *testing.T) {
	valid := []string{"v0.0.1", "v1.2.3", "v1.2.3+4", "v1.2.3-dev"}
	for _, item := range valid {
		if !IsSemantic(item) {
			t.Fatalf("%q should be semantic", item)
		}
	}
	invalid := []string{"", "dev", "1.2.3", "v1.2", "v1.2.3.4", "vx.y.z", "v1.2.3-"}
	for _, item := range invalid {
		if IsSemantic(item) {
			t.Fatalf("%q should not be semantic", item)
		}
	}
}
