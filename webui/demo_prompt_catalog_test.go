// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 演示站没有后端，提示词目录是前端仓库里的一份 JSON。它必须和登记表逐字相同：
// 以前那份是手写的三条样例，演示站的人设 YAML 就只列三段，看起来像是功能不全。
// 登记表一变这个测试就失败，按提示重新生成：
//
//	DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoPromptCatalogInSync
func TestDemoPromptCatalogInSync(t *testing.T) {
	path := filepath.Join("..", "frontend-next", "src", "demo-prompt-catalog.json")
	want, err := json.MarshalIndent(map[string]any{
		"groups":    assistant.PromptGroups(),
		"prompts":   assistant.PromptSpecs(),
		"max_runes": assistant.PromptOverrideMaxRunes,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("DIANA_UPDATE_DEMO_CATALOG") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s 和提示词登记表不一致，运行 DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoPromptCatalogInSync 重新生成", path)
	}
}
