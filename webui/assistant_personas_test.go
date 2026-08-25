// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	// 空库要回空数组而不是 null，前端才不用为这一种情况写分支。
	if listed.Personas == nil || len(listed.Personas) != 0 {
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
	if saved.Persona.ID == "" || len(saved.Personas) != 1 {
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
	if len(saved.Personas) != 1 || saved.Personas[0].Name != "猫娘 v2" {
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
	if len(listed.Personas) != 0 {
		t.Fatalf("library after delete = %#v", listed.Personas)
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
