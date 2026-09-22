// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"
)

// 分类漏项的代价线上见过：preset_save 没被认成「改了定义」，从预设装的 MCP 要等重启
// 才出现。这里把两件事都钉住——每个操作都被归类，且归类结果和实际行为一致。
func TestExtensionOperationClassification(t *testing.T) {
	definition := map[string]bool{"save": true, "preset_save": true, "delete": true}
	state := map[string]bool{"enabled": true, "members": true, "audience": true, "residency": true, "preset_hide": true, "preset_show": true}
	for _, operation := range ExtensionOperations {
		if got := ExtensionOperationChangesDefinition(operation); got != definition[operation] {
			t.Fatalf("%s 改定义 = %v，期望 %v", operation, got, definition[operation])
		}
		if want := definition[operation] || state[operation]; ExtensionOperationMutatesState(operation) != want {
			t.Fatalf("%s 改状态 = %v，期望 %v", operation, !want, want)
		}
	}
	if ExtensionOperationMutatesState("nope") || ExtensionOperationChangesDefinition("nope") {
		t.Fatal("不认识的操作一律按不改处理")
	}
}

// ExtensionOperations 得跟着 AdministerExtensions 一起长：漏登记的操作在这里就会被抓到，
// 而不是等某个调用方悄悄少做一件事。
func TestExtensionOperationsCoverDispatcher(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	known := map[string]bool{}
	for _, operation := range ExtensionOperations {
		known[operation] = true
	}
	for _, operation := range ExtensionOperations {
		// 用一个不存在的名字调用：认识的操作会报「不存在」之类的业务错误，
		// 不认识的操作一律报「不支持的操作」。
		_, err := AdministerExtensions(context.Background(), cfg, ExtensionAdminRequest{Operation: operation, Kind: "mcp", Name: "missing-service"})
		if err != nil && strings.Contains(err.Error(), "不支持的操作") {
			t.Fatalf("%s 登记在 ExtensionOperations 里，分发器却不认识", operation)
		}
	}
	if _, err := AdministerExtensions(context.Background(), cfg, ExtensionAdminRequest{Operation: "definitely_not_an_operation", Kind: "mcp", Name: "x"}); err == nil {
		t.Fatal("没登记的操作应当被分发器拒绝")
	}
}
