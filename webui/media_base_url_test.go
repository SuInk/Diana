// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

func requestMediaBaseURL(router *gin.Engine, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/system/media-base-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func mediaBaseURLResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

// 保存→热生效→重启恢复→清空回落，全链路走一遍；config.yaml 的值只在数据库
// 没有记录时作为兜底，且永远能被 WebUI 保存的空值「清除」回自动推断。
func TestMediaBaseURLSaveApplyRestartAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	var applied []string
	apply := func(baseURL string) { applied = append(applied, baseURL) }

	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewMediaBaseURLHandler(context.Background(), store, "http://from-config.example:18080/media/resolver", apply)
	if err != nil {
		t.Fatal(err)
	}
	// 数据库无记录：回落 config.yaml，apply 立即推入生效值。
	if got := mediaBaseURLResponse(t, requestMediaBaseURL(ginRouter(h), http.MethodGet, "")); got["base_url"] != "http://from-config.example:18080/media/resolver" || got["source"] != "config" {
		t.Fatalf("initial = %+v", got)
	}
	if len(applied) != 1 || applied[0] != "http://from-config.example:18080/media/resolver" {
		t.Fatalf("applied = %v", applied)
	}

	// 保存合法值：路径缺省补 /media/resolver，热生效，数据库优先于 config.yaml。
	w := requestMediaBaseURL(ginRouter(h), http.MethodPost, `{"base_url":"http://192.168.1.10:18080"}`)
	got := mediaBaseURLResponse(t, w)
	if w.Code != 200 || got["base_url"] != "http://192.168.1.10:18080/media/resolver" || got["source"] != "database" {
		t.Fatalf("save = %d %+v", w.Code, got)
	}
	if applied[len(applied)-1] != "http://192.168.1.10:18080/media/resolver" {
		t.Fatalf("hot apply = %v", applied)
	}

	// 重启后从数据库恢复，不再看 config.yaml。
	store.Close()
	reopened, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	applied = nil
	restored, err := NewMediaBaseURLHandler(context.Background(), reopened, "http://from-config.example:18080/media/resolver", apply)
	if err != nil {
		t.Fatal(err)
	}
	if got := mediaBaseURLResponse(t, requestMediaBaseURL(ginRouter(restored), http.MethodGet, "")); got["base_url"] != "http://192.168.1.10:18080/media/resolver" || got["source"] != "database" {
		t.Fatalf("restored = %+v", got)
	}

	// 清空：回落 config.yaml；config.yaml 也为空时回到自动推断。
	w = requestMediaBaseURL(ginRouter(restored), http.MethodPost, `{"base_url":""}`)
	if got := mediaBaseURLResponse(t, w); w.Code != 200 || got["source"] != "config" {
		t.Fatalf("clear = %d %+v", w.Code, got)
	}
	storeEmpty, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storeEmpty.Close()
	noConfig, err := NewMediaBaseURLHandler(context.Background(), storeEmpty, "", apply)
	if err != nil {
		t.Fatal(err)
	}
	if got := mediaBaseURLResponse(t, requestMediaBaseURL(ginRouter(noConfig), http.MethodGet, "")); got["base_url"] != "" || got["source"] != "auto" {
		t.Fatalf("auto = %+v", got)
	}
}

func TestMediaBaseURLRejectsInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base, err := NewMediaBaseURLHandler(context.Background(), store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	router := ginRouter(base)
	for _, body := range []string{
		`{"base_url":"ftp://example.com/media/resolver"}`,
		`{"base_url":"http:///media/resolver"}`,
		`{"base_url":"http://example.com/custom/path"}`,
		`{"base_url":123}`,
	} {
		if w := requestMediaBaseURL(router, http.MethodPost, body); w.Code != 400 {
			t.Errorf("accepted %s: %d", body, w.Code)
		}
	}
	if got := mediaBaseURLResponse(t, requestMediaBaseURL(router, http.MethodGet, "")); got["base_url"] != "" || got["source"] != "auto" {
		t.Fatalf("invalid saves changed state: %+v", got)
	}
}

func ginRouter(h *MediaBaseURLHandler) *gin.Engine {
	router := gin.New()
	h.Register(router)
	return router
}
