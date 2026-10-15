package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestResolverCookieFileEditor(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(handler)
	if _, err := runtime.Plugins().UpdateSettingsForProfile(assistant.ResolverPluginID, "", map[string]any{"max_links": 7, "bili_sessdata": "existing-session"}, nil); err != nil {
		t.Fatal(err)
	}
	call := func(route string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/resolver/cookie-file"+route, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("/read", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	content := "# Netscape HTTP Cookie File\r\n#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t0\tSID\ttest-cookie-secret\r\n"
	rec := call("", map[string]string{"content": content, "path": "/ignored/path"})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "test-cookie-secret") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Draft checks expose only counts and never activate or overwrite a file.
	check := call("/check", map[string]any{"content": content})
	if check.Code != 200 || strings.Contains(check.Body.String(), "test-cookie-secret") || !strings.Contains(check.Body.String(), `"youtube_count":1`) {
		t.Fatal(check.Code, check.Body.String())
	}
	draft := "\ufeff\r\n# HTTP Cookie File\r\n\r\n#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1\tSID\t  keep spaces  \r\n.youtube.com.evil.example\tFALSE\t/\tFALSE\t0\tother\tvalue\r\n"
	formatted := call("/check", map[string]any{"content": draft, "format": true})
	var result struct {
		Content string `json:"content"`
		Count   int    `json:"count"`
		YouTube int    `json:"youtube_count"`
		Expired int    `json:"expired_count"`
	}
	if formatted.Code != 200 || json.Unmarshal(formatted.Body.Bytes(), &result) != nil {
		t.Fatal(formatted.Code, formatted.Body.String())
	}
	if result.Count != 2 || result.YouTube != 1 || result.Expired != 1 || !strings.Contains(result.Content, "#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1\tSID\t  keep spaces  \n") || !strings.HasPrefix(result.Content, "# Netscape HTTP Cookie File\n") {
		t.Fatalf("unexpected draft result: count=%d youtube=%d expired=%d", result.Count, result.YouTube, result.Expired)
	}
	if again := formatResolverCookies(result.Content); again != result.Content {
		t.Fatal("formatting is not idempotent")
	}
	if bad := call("/check", map[string]any{"content": "SID=secret", "format": true}); bad.Code != 400 {
		t.Fatal("guessed missing Netscape metadata")
	}
	path, _ := handler.resolverCookieFilePath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	_, settings, _ := runtime.Plugins().PluginForConfiguration(assistant.ResolverPluginID, "")
	if settings.Int("max_links", 0) != 7 || settings.String("bili_sessdata", "") != "existing-session" {
		t.Fatal("save changed other settings")
	}
	if settings.String("ytdlp_cookies_path", "") != path {
		t.Fatal("managed path not activated")
	}
	rec = call("/read", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "test-cookie-secret") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"SID=test-cookie-secret", "# Netscape HTTP Cookie File\n", "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\tbad\tSID\tsecret", strings.Repeat("x", resolverCookieFileLimit+1)} {
		rec = call("", map[string]string{"content": bad})
		if rec.Code != 400 || strings.Contains(rec.Body.String(), "test-cookie-secret") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	saved, _ := os.ReadFile(path)
	if string(saved) != strings.ReplaceAll(content, "\r\n", "\n") {
		t.Fatal("invalid save changed existing cookies")
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("external secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Skip(err)
	}
	if rec := call("/read", nil); rec.Code != 400 || strings.Contains(rec.Body.String(), "external secret") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := call("", map[string]string{"content": content}); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	outside, _ := os.ReadFile(external)
	if string(outside) != "external secret" {
		t.Fatal("overwrote external file")
	}
}
