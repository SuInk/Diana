package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func managedFixture(t *testing.T) (*gin.Engine, *TrustedLoginHandler, string) {
	t.Helper()
	_, _, owner, _ := trustedFixture(t)
	h, err := NewManagedTrustedLoginHandler(owner, filepath.Join(t.TempDir(), "trusted-login.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := owner.auth.IssueSession()
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(owner.auth.Middleware())
	h.Register(r)
	return r, h, token
}
func managementRequest(r http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func TestTrustedSettingsLifecycleAndPersistence(t *testing.T) {
	r, h, token := managedFixture(t)
	req := func(method, path, body string) *httptest.ResponseRecorder {
		return managementRequest(r, token, method, path, body)
	}
	root := "/api/auth/apps/settings"
	created := req("POST", root+"/apps", `{"id":"ruhua","name":"入画","profile_id":"bot-a","disabled":false}`)
	if created.Code != 200 {
		t.Fatal(created.Code, created.Body)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Secret) != 64 {
		t.Fatal("missing one-time credential")
	}
	disk, err := os.ReadFile(h.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), out.Secret) {
		t.Fatal("plaintext persisted")
	}
	list := req("GET", root, "")
	if strings.Contains(list.Body.String(), out.Secret) || strings.Contains(list.Body.String(), "secret_sha256") {
		t.Fatal("credential disclosed in list")
	}
	pair := func(secret string, path string, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", path, strings.NewReader(body))
		request.SetBasicAuth("ruhua", secret)
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, request)
		return w
	}
	if pair(out.Secret, "/api/auth/apps/pair", "").Code != 401 {
		t.Fatal("default enabled")
	}
	if w := req("PUT", root, `{"enabled":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var p map[string]any
	w := pair(out.Secret, "/api/auth/apps/pair", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	if !h.ConsumePrivateMessage(context.Background(), trustedEvent(), p["command"].(string)) {
		t.Fatal("confirmation failed")
	}
	w = pair(out.Secret, "/api/auth/apps/pair/status", `{"poll_token":"`+p["poll_token"].(string)+`"}`)
	if !strings.Contains(w.Body.String(), `"approved":true`) {
		t.Fatal(w.Body)
	}
	restarted, err := NewManagedTrustedLoginHandler(h.owner, h.policyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.enabled || len(restarted.apps) != 1 || len(restarted.records) != 1 {
		t.Fatal("settings or records lost on restart")
	}
	// Rotation changes only the credential and destroys pending pairings.
	h.lastCreate["ruhua"] = time.Time{}
	w = pair(out.Secret, "/api/auth/apps/pair", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rotated := req("POST", root+"/apps/ruhua/rotate", `{}`)
	if rotated.Code != 200 {
		t.Fatal(rotated.Code, rotated.Body)
	}
	if len(h.pairs) != 0 || pair(out.Secret, "/api/auth/apps/pair", "").Code != 401 {
		t.Fatal("old credential or pending pairing survived rotation")
	}
	var newKey struct {
		Secret string `json:"secret"`
	}
	json.Unmarshal(rotated.Body.Bytes(), &newKey)
	if newKey.Secret == out.Secret || newKey.Secret == "" {
		t.Fatal("rotation failed")
	}
	if w := req("PATCH", root+"/apps/ruhua", `{"id":"ruhua","name":"入画","profile_id":"bot-a","disabled":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if pair(newKey.Secret, "/api/auth/apps/pair", "").Code != 401 {
		t.Fatal("disabled app authorized")
	}
	if w := req("PUT", root, `{"enabled":false}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	restarted, err = NewManagedTrustedLoginHandler(h.owner, h.policyPath, []TrustedLoginApp{})
	if err != nil || restarted.enabled {
		t.Fatal("disabled state lost")
	}
}
func TestTrustedSettingsAuthorizationAndFailedSave(t *testing.T) {
	r, h, token := managedFixture(t)
	root := "/api/auth/apps/settings"
	if w := managementRequest(r, "", "GET", root, ""); w.Code != 401 {
		t.Fatal("anonymous admin access")
	}
	if w := trustedRequest(r, "ruhua", root, `{}`); w.Code == 200 {
		t.Fatal("application credential got admin access")
	}
	request := httptest.NewRequest("PUT", root, strings.NewReader(`{"enabled":true}`))
	request.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	for _, body := range []string{`{}`, `{"enabled":true,"unknown":1}`, `{"enabled":true} {}`} {
		if w := managementRequest(r, token, "PUT", root, body); w.Code != 400 {
			t.Fatal("malformed policy accepted")
		}
	}
	h.policyPath = filepath.Join(h.policyPath, "not-a-directory.json")
	w = managementRequest(r, token, "PUT", root, `{"enabled":true}`)
	if w.Code != 500 || h.enabled {
		t.Fatal("failed persistence changed runtime policy")
	}
}
