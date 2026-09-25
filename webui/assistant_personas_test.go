// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func personaRequest(t *testing.T, router http.Handler, method string, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPersonaLibraryCreateUpdateDelete(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodGet, "/api/assistant/personas", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed personaListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	// 空库也带着内置人设，内置的排在最前面、只读。
	builtins := assistant.BuiltinPersonas()
	if len(listed.Personas) != len(builtins) || listed.Personas[0].ID != "builtin:default" || !listed.Personas[0].Builtin {
		t.Fatalf("empty library = %#v", listed.Personas)
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{
		Persona: assistant.Persona{Name: "猫娘", ReplyStyle: assistant.ReplyStyleCatgirl, SelfReference: "我", SentenceEnders: "喵,喵~"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Persona  assistant.Persona   `json:"persona"`
		Personas []assistant.Persona `json:"personas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Persona.ID == "" || len(userPersonas(saved.Personas)) != 1 {
		t.Fatalf("saved = %#v", saved)
	}

	// 带同一个 ID 再存是改，库里仍然只有一套。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{
		Persona: assistant.Persona{ID: saved.Persona.ID, Name: "猫娘 v2", SystemPrompt: "你是一只猫"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if mine := userPersonas(saved.Personas); len(mine) != 1 || mine[0].Name != "猫娘 v2" {
		t.Fatalf("update produced %#v", saved.Personas)
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/delete", personaDeletePayload{ID: saved.Persona.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = personaRequest(t, router, http.MethodGet, "/api/assistant/personas", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(userPersonas(listed.Personas)) != 0 {
		t.Fatalf("library after delete = %#v", listed.Personas)
	}

	// 内置人设只读：拿它的 ID 保存要被拒。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{
		Persona: assistant.Persona{ID: "builtin:jiaran", Name: "嘉然", SystemPrompt: "改掉"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin save status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 只有名字没内容的空壳不该进库：列表里点开是空的，还占一格。
func TestPersonaLibraryRejectsEmptyPersona(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{
		Persona: assistant.Persona{Name: "空壳"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPersonaLibraryImportMergesWithoutOverwriting(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{
		Persona: assistant.Persona{Name: "猫娘", SystemPrompt: "我自己调的这一版"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 同名但正文不同：改名导入，不覆盖本地那份。
	var result personaImportResponse
	for _, source := range []string{"# 猫娘\n\n别人机器上的那一版", "# 技术群管\n\n话不多"} {
		rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import", personaImportPayload{Source: source, Filename: "x.md"})
		if rec.Code != http.StatusOK {
			t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	if len(userPersonas(result.Personas)) != 3 {
		t.Fatalf("library = %#v", result.Personas)
	}
	for _, persona := range userPersonas(result.Personas) {
		if persona.Name == "猫娘" && persona.SystemPrompt != "我自己调的这一版" {
			t.Fatalf("本地那份被覆盖了：%#v", persona)
		}
	}
	// 同一份再导一次：跳过，不攒副本。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import", personaImportPayload{Source: "# 技术群管\n\n话不多", Filename: "x.md"})
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Skipped != 1 || result.Imported != 0 {
		t.Fatalf("duplicate import = %#v err=%v", result, err)
	}
}

// 现在的格式是一份 SOUL.md：按文件名认格式，名字取一级标题。
func TestPersonaLibraryImportsSoulMarkdown(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import", personaImportPayload{
		Source:   "# 小满\n\n## 概述\n\n她说话很慢。",
		Filename: "xiaoman.md",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result personaImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	mine := userPersonas(result.Personas)
	if result.Imported != 1 || len(mine) != 1 || mine[0].Name != "小满" || !strings.HasPrefix(mine[0].SystemPrompt, "# 小满") {
		t.Fatalf("result = %#v", result)
	}
}

func userPersonas(personas []assistant.Persona) []assistant.Persona {
	mine := make([]assistant.Persona, 0, len(personas))
	for _, persona := range personas {
		if !persona.Builtin {
			mine = append(mine, persona)
		}
	}
	return mine
}

// 空文件要给出明确错误，而不是当成「导入成功 0 套」。
func TestPersonaLibraryImportRejectsEmptyFile(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import", personaImportPayload{Source: "  ", Filename: "empty.md"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
