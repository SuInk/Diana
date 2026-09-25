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

// 内置人设同理：演示站的人设库直接读这份 JSON，SOUL.md 一改就得重新生成，
// 不然演示站看到的是旧文案，或者干脆少几份（以前手抄了三份，另外三份演示站看不到）。
// 和提示词目录共用一个环境变量：
//
//	DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoBuiltinSoulsInSync
func TestDemoBuiltinSoulsInSync(t *testing.T) {
	path := filepath.Join("..", "frontend-next", "src", "demo-builtin-souls.json")
	type demoSoul struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Builtin      bool   `json:"builtin"`
		SystemPrompt string `json:"system_prompt"`
	}
	var souls []demoSoul
	for _, persona := range assistant.BuiltinPersonas() {
		souls = append(souls, demoSoul{ID: persona.ID, Name: persona.Name, Builtin: true, SystemPrompt: persona.SystemPrompt})
	}
	want, err := json.MarshalIndent(souls, "", "  ")
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
		t.Fatalf("%s 和内置 SOUL.md 不一致，运行 DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoBuiltinSoulsInSync 重新生成", path)
	}
}
