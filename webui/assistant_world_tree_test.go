// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestWorldTreeCreateUpdateDelete(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodGet, "/api/assistant/world-tree", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed worldTreeListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	// 空树要回空数组而不是 null，前端才不用为这一种情况写分支。
	if listed.Nodes == nil || len(listed.Nodes) != 0 {
		t.Fatalf("empty tree = %#v", listed.Nodes)
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree", worldTreeSavePayload{
		Node: assistant.WorldTreeNode{Title: "枝江", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Node  assistant.WorldTreeNode   `json:"node"`
		Nodes []assistant.WorldTreeNode `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Node.ID == "" || len(saved.Nodes) != 1 {
		t.Fatalf("saved = %#v", saved)
	}

	// 挂一个子节点，删掉父节点后它要接到根上。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree", worldTreeSavePayload{
		Node: assistant.WorldTreeNode{ParentID: saved.Node.ID, Title: "港口", Content: "枝江港常年有雾。", Keywords: []string{"港口"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save child status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree/delete", worldTreeDeletePayload{ID: saved.Node.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var afterDelete struct {
		Nodes []assistant.WorldTreeNode `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatal(err)
	}
	if len(afterDelete.Nodes) != 1 || afterDelete.Nodes[0].Title != "港口" || afterDelete.Nodes[0].ParentID != "" {
		t.Fatalf("after delete = %#v", afterDelete.Nodes)
	}

	// 没标题的节点要报 400，不能静默丢弃。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree", worldTreeSavePayload{
		Node: assistant.WorldTreeNode{Content: "有正文但没标题"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("titleless save status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorldTreeImport(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)

	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree/import", worldTreeImportPayload{
		Version: 1,
		Nodes: []assistant.WorldTreeNode{
			{ID: "file-root", Title: "世界", Content: "总纲", AlwaysOn: true},
			{ID: "file-child", ParentID: "file-root", Title: "枝江", Content: "城市设定", Keywords: []string{"枝江"}},
			{Content: "没标题"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var imported worldTreeImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || imported.Dropped != 1 || len(imported.Nodes) != 2 {
		t.Fatalf("import result = %#v", imported)
	}
	// 文件内部的父子引用要按新 ID 重连。
	byTitle := map[string]assistant.WorldTreeNode{}
	for _, node := range imported.Nodes {
		byTitle[node.Title] = node
	}
	if byTitle["枝江"].ParentID != byTitle["世界"].ID {
		t.Fatalf("parent not remapped: %#v", imported.Nodes)
	}

	// 空文件要报 400。
	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/world-tree/import", worldTreeImportPayload{Version: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty import status=%d body=%s", rec.Code, rec.Body.String())
	}
}
