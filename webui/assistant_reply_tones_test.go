// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestReplyToneLibraryCreateUpdateDelete(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)
	rec := personaRequest(t, router, http.MethodGet, "/api/assistant/reply-tones", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed replyToneListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Presets) != 1 || !listed.Presets[0].Builtin {
		t.Fatalf("initial=%#v", listed.Presets)
	}
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/reply-tones", replyToneSavePayload{Preset: assistant.ReplyTonePreset{Name: "短句", Text: "少用句号"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Preset  assistant.ReplyTonePreset   `json:"preset"`
		Presets []assistant.ReplyTonePreset `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Preset.ID == "" || len(saved.Presets) != 2 {
		t.Fatalf("saved=%#v", saved)
	}
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/reply-tones/delete", replyToneDeletePayload{ID: saved.Preset.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/reply-tones/delete", replyToneDeletePayload{ID: "builtin:default"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}
