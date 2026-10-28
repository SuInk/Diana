// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"strings"
	"testing"
)

// 机器人和控制台同权：能在控制台改的，主人私聊里也都能让机器人改。
func TestRuntimeConfigFieldsBotMatchesAdmin(t *testing.T) {
	seen := map[string]bool{}
	for _, field := range runtimeConfigFields() {
		if seen[field.Key] {
			t.Fatalf("重复的配置项 %s", field.Key)
		}
		seen[field.Key] = true
		if field.BotEditable != field.Editable {
			t.Fatalf("%s: bot_editable=%v editable=%v", field.Key, field.BotEditable, field.Editable)
		}
		if strings.HasPrefix(field.Key, "admin.") && field.Editable {
			t.Fatalf("%s 只在首启使用，不该可改", field.Key)
		}
	}
	if _, err := newRuntimeConfigManager(appConfig{}, "", "18080"); err != nil {
		t.Fatal(err)
	}
}
