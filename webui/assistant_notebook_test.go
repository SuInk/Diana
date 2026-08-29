// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func newNotebookTestRouter(t *testing.T) (*storage.SQLiteStore, http.Handler) {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "notebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.SetSQLiteStore(store)
	return store, botTestRouter(handler)
}

func notebookRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// 控制台是笔记本的人工兜底：机器人记错了一个梗，群里没人会专门去纠正它。
// 这条覆盖新增、改、作废、恢复的整条链路，并确认每一步都留了修订记录。
func TestNotebookConsoleEditsLeaveRevisions(t *testing.T) {
	_, router := newNotebookTestRouter(t)

	rec := notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook", map[string]any{
		"scope": "group:20001", "term": "typo姐", "meaning": "打错字最多的人", "aliases": []string{"typo"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var entry assistant.NotebookEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Version != 1 || entry.Status != assistant.NotebookStatusActive {
		t.Fatalf("entry = %+v", entry)
	}

	rec = notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook", map[string]any{
		"scope": "group:20001", "term": "typo姐", "meaning": "现在是夸人", "note": "用法反转了",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = notebookRequest(t, router, http.MethodGet, "/api/assistant/notebook/entry?scope=group:20001&term=typo姐", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Meaning != "现在是夸人" || len(entry.Revisions) != 2 {
		t.Fatalf("detail = %+v", entry)
	}
	// 控制台改的也算一次修订，不是绕过历史的后门。
	if entry.Revisions[0].Note != "更新：用法反转了" {
		t.Fatalf("revision = %+v", entry.Revisions[0])
	}

	rec = notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook/delete", map[string]any{
		"scope": "group:20001", "term": "typo姐", "note": "记错了",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = notebookRequest(t, router, http.MethodGet, "/api/assistant/notebook?scope=group:20001", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed notebookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 0 {
		t.Fatalf("作废的条目不该出现在默认列表: %+v", listed.Entries)
	}
	if len(listed.Scopes) != 1 || listed.Scopes[0].DeletedCount != 1 {
		t.Fatalf("scopes = %+v", listed.Scopes)
	}

	rec = notebookRequest(t, router, http.MethodGet, "/api/assistant/notebook?scope=group:20001&include_deleted=true", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Status != assistant.NotebookStatusDeleted {
		t.Fatalf("include_deleted = %+v", listed.Entries)
	}

	rec = notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook/restore", map[string]any{
		"scope": "group:20001", "term": "typo姐",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Status != assistant.NotebookStatusActive || entry.Meaning != "现在是夸人" {
		t.Fatalf("restored = %+v", entry)
	}
}

// 没选作用域时默认给最近更新的那本，省得页面开出来是空的。
func TestNotebookConsoleDefaultsToMostRecentScope(t *testing.T) {
	_, router := newNotebookTestRouter(t)
	for _, scope := range []string{"group:20001", "group:20002"} {
		if rec := notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook", map[string]any{
			"scope": scope, "term": "梗" + scope, "meaning": "释义",
		}); rec.Code != http.StatusOK {
			t.Fatalf("seed %s status=%d body=%s", scope, rec.Code, rec.Body.String())
		}
	}
	rec := notebookRequest(t, router, http.MethodGet, "/api/assistant/notebook", nil)
	var listed notebookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Scope != "group:20002" || len(listed.Entries) != 1 {
		t.Fatalf("listed = %+v", listed)
	}
}

// 缺参数和空释义都要当场拒绝：控制台写进去的东西同样会被机器人当真。
func TestNotebookConsoleRejectsIncompleteInput(t *testing.T) {
	_, router := newNotebookTestRouter(t)
	for _, body := range []map[string]any{
		{"scope": "", "term": "梗", "meaning": "释义"},
		{"scope": "group:1", "term": "", "meaning": "释义"},
		{"scope": "group:1", "term": "梗", "meaning": "   "},
	} {
		if rec := notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %v status=%d", body, rec.Code)
		}
	}
	if rec := notebookRequest(t, router, http.MethodPost, "/api/assistant/notebook/delete", map[string]any{
		"scope": "group:1", "term": "不存在的词",
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status=%d", rec.Code)
	}
}
