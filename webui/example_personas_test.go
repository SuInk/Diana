// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	yaml "go.yaml.in/yaml/v4"
)

// examples/personas 里的人设文件必须能直接导入。人设文件要列出全部提示词，登记表一变
// （加一段、删一段）示例就过期，这个测试就失败，按提示重新生成：
//
//	DIANA_UPDATE_EXAMPLE_PERSONAS=1 go test ./webui -run TestExamplePersonasImport
//
// 重新生成时宽松地读出现有内容（不认识的提示词键丢掉，缺的按默认补），再按当前格式写回，
// 示例里写的人设正文、品格和改过的提示词都保留。
func TestExamplePersonasImport(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "examples", "personas", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no example personas found: %v", err)
	}
	update := os.Getenv("DIANA_UPDATE_EXAMPLE_PERSONAS") == "1"
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if update {
			personas := looseExamplePersonas(t, raw)
			out, err := assistant.RenderPersonaYAML(personas)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, out, 0o644); err != nil {
				t.Fatal(err)
			}
			raw = out
		}
		if _, err := assistant.ParsePersonaDocument(raw); err != nil {
			t.Errorf("%s: %v（运行 DIANA_UPDATE_EXAMPLE_PERSONAS=1 go test ./webui -run TestExamplePersonasImport 重新生成）", filepath.Base(file), err)
		}
	}
}

func looseExamplePersonas(t *testing.T, raw []byte) []assistant.Persona {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	delete(root, "format_version")
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Personas []assistant.Persona `json:"personas"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Personas) == 0 {
		var single assistant.Persona
		if err := json.Unmarshal(encoded, &single); err != nil {
			t.Fatal(err)
		}
		document.Personas = []assistant.Persona{single}
	}
	for index, persona := range document.Personas {
		persona = persona.Normalized()
		persona.ID = ""
		if strings.TrimSpace(persona.Name) == "" {
			t.Fatal("example persona without name")
		}
		document.Personas[index] = persona
	}
	return document.Personas
}
