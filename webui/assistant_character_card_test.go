// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCharacterCardImportCreatesPersonaAndBook(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	card := `{
		"spec": "chara_card_v2",
		"data": {
			"name": "然然",
			"description": "{{char}}是枝江的看板娘。",
			"first_mes": "来啦！",
			"character_book": {
				"name": "枝江设定",
				"entries": [{"keys": ["港口"], "name": "港口", "content": "枝江港常年有雾。", "insertion_order": 1}]
			}
		}
	}`
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import-card", characterCardImportPayload{
		CardBase64: base64.StdEncoding.EncodeToString([]byte(card)),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var imported characterCardImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Persona == nil || imported.Persona.Name != "然然" || len(imported.Personas) != 1 {
		t.Fatalf("persona = %#v", imported)
	}
	if imported.BookName != "枝江设定" || imported.BookImported != 1 || len(imported.Nodes) != 1 {
		t.Fatalf("book = %#v", imported)
	}

	// 同一张卡再导一次：人设按同名同内容跳过，世界书条目仍会追加（导入只增不减）。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import-card", characterCardImportPayload{
		CardBase64: base64.StdEncoding.EncodeToString([]byte(card)),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 复用结构体前先清零：响应里被 omitempty 省略的字段不会覆盖旧值。
	imported = characterCardImportResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Persona != nil || imported.Skipped != 1 || len(imported.Personas) != 1 {
		t.Fatalf("repeat persona = %#v", imported)
	}

	// 垃圾输入报 400。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import-card", characterCardImportPayload{
		CardBase64: base64.StdEncoding.EncodeToString([]byte("not a card")),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/import-card", characterCardImportPayload{CardBase64: "%%%"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad base64 status=%d body=%s", rec.Code, rec.Body.String())
	}
}
