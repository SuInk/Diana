// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestWorldBookCreateUpdateDelete(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodGet, "/api/assistant/world-book", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed worldBookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	// 空树要回空数组而不是 null，前端才不用为这一种情况写分支。
	if listed.Nodes == nil || len(listed.Nodes) != 0 {
		t.Fatalf("empty tree = %#v", listed.Nodes)
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book", worldBookSavePayload{
		Node: assistant.WorldBookNode{Title: "枝江", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Node  assistant.WorldBookNode   `json:"node"`
		Nodes []assistant.WorldBookNode `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Node.ID == "" || len(saved.Nodes) != 1 {
		t.Fatalf("saved = %#v", saved)
	}

	// 挂一个子节点，删掉父节点后它要接到根上。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book", worldBookSavePayload{
		Node: assistant.WorldBookNode{ParentID: saved.Node.ID, Title: "港口", Content: "枝江港常年有雾。", Keywords: []string{"港口"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save child status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book/delete", worldBookDeletePayload{ID: saved.Node.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var afterDelete struct {
		Nodes []assistant.WorldBookNode `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatal(err)
	}
	if len(afterDelete.Nodes) != 1 || afterDelete.Nodes[0].Title != "港口" || afterDelete.Nodes[0].ParentID != "" {
		t.Fatalf("after delete = %#v", afterDelete.Nodes)
	}

	// 没标题的节点要报 400，不能静默丢弃。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book", worldBookSavePayload{
		Node: assistant.WorldBookNode{Content: "有正文但没标题"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("titleless save status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorldBookImport(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/world-book/import", worldBookImportPayload{
		Version: 1,
		Nodes: []assistant.WorldBookNode{
			{ID: "file-root", Title: "世界", Content: "总纲", AlwaysOn: true},
			{ID: "file-child", ParentID: "file-root", Title: "枝江", Content: "城市设定", Keywords: []string{"枝江"}},
			{Content: "没标题"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var imported worldBookImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || imported.Dropped != 1 || len(imported.Nodes) != 2 {
		t.Fatalf("import result = %#v", imported)
	}
	// 文件内部的父子引用要按新 ID 重连。
	byTitle := map[string]assistant.WorldBookNode{}
	for _, node := range imported.Nodes {
		byTitle[node.Title] = node
	}
	if byTitle["枝江"].ParentID != byTitle["世界"].ID {
		t.Fatalf("parent not remapped: %#v", imported.Nodes)
	}

	// 空文件要报 400。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book/import", worldBookImportPayload{Version: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty import status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// SillyTavern 世界书文件的 entries 直接交给后端转换导入。
func TestWorldBookImportSillyTavernEntries(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/world-book/import", map[string]any{
		"entries": map[string]any{
			"0": map[string]any{"uid": 0, "key": []string{}, "comment": "世界", "content": "总纲。", "constant": true, "order": 100},
			"1": map[string]any{"uid": 1, "key": []string{"港口"}, "comment": "港口", "content": "常年有雾。", "order": 200},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var imported worldBookImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || len(imported.Nodes) != 2 {
		t.Fatalf("import result = %#v", imported)
	}
	if imported.Nodes[0].Title != "世界" || !imported.Nodes[0].AlwaysOn {
		t.Fatalf("nodes = %#v", imported.Nodes)
	}

	// 认不出来的 entries 报 400，不静默导入零条。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-book/import", map[string]any{"entries": "垃圾"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid entries status=%d body=%s", rec.Code, rec.Body.String())
	}
}
