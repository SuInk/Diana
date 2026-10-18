package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

const trustedTestSecret = "0123456789abcdef0123456789abcdef"

func trustedFixture(t *testing.T) (*gin.Engine, *TrustedLoginHandler, *OwnerLoginHandler, *fakeOwnerRuntime) {
	t.Helper()
	runtime := ownerLoginRuntime()
	runtime.cfg.ID = "bot-a"
	_, manager, owner := newOwnerLoginTestRouter(t, runtime)
	hash := sha256.Sum256([]byte(trustedTestSecret))
	h, err := NewTrustedLoginHandler(owner, []TrustedLoginApp{{ID: "ruhua", Name: "入画", SecretSHA256: hex.EncodeToString(hash[:]), ProfileID: "bot-a"}, {ID: "other", Name: "另一应用", SecretSHA256: hex.EncodeToString(hash[:]), ProfileID: "bot-a"}})
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(manager.Middleware())
	owner.Register(r)
	h.Register(r)
	return r, h, owner, runtime
}
func trustedRequest(r http.Handler, app, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(app, trustedTestSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
func trustedCreate(t *testing.T, r http.Handler) map[string]any {
	t.Helper()
	rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair", "")
	if rec.Code != 200 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var b map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
func trustedEvent() assistant.MessageEvent {
	return assistant.MessageEvent{Kind: assistant.EventKindPrivate, ProfileID: "bot-a", Platform: assistant.PlatformOneBotV11, UserID: "10001"}
}
func TestTrustedLoginIsolationAndSingleConsumption(t *testing.T) {
	r, h, owner, runtime := trustedFixture(t)
	b := trustedCreate(t, r)
	body := `{"poll_token":"` + b["poll_token"].(string) + `"}`
	if owner.ConsumePrivateMessage(context.Background(), trustedEvent(), b["command"].(string)) {
		t.Fatal("external command reached console")
	}
	if !h.ConsumePrivateMessage(context.Background(), trustedEvent(), b["command"].(string)) {
		t.Fatal("not confirmed")
	}
	if !strings.Contains(runtime.sentTexts()[0], "入画（ruhua）") {
		t.Fatal("receipt missing application")
	}
	for _, rec := range []*httptest.ResponseRecorder{claimOwnerPairing(r, b["code"].(string)), pollOwnerPairing(t, r, b["poll_token"].(string))} {
		if rec.Header().Get("Set-Cookie") != "" || strings.Contains(rec.Body.String(), `"approved":true`) {
			t.Fatal("console session escaped")
		}
	}
	other := trustedRequest(r, "other", "/api/auth/apps/pair/status", body)
	if !strings.Contains(other.Body.String(), `"expired":true`) {
		t.Fatal("cross application read")
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair/status", body)
			if rec.Header().Get("Set-Cookie") != "" {
				t.Error("external login issued cookie")
			}
			if strings.Contains(rec.Body.String(), `"approved":true`) {
				winners.Add(1)
				if !strings.Contains(rec.Body.String(), `"user_id":"10001"`) {
					t.Error("missing numeric identity")
				}
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("consumed %d times", winners.Load())
	}
}
func TestTrustedLoginSenderAndExpiry(t *testing.T) {
	r, h, _, runtime := trustedFixture(t)
	b := trustedCreate(t, r)
	command := b["command"].(string)
	for _, mutate := range []func(*assistant.MessageEvent){func(e *assistant.MessageEvent) { e.UserID = "999" }, func(e *assistant.MessageEvent) { e.ProfileID = "bot-b" }, func(e *assistant.MessageEvent) { e.Kind = assistant.EventKindGroup }, func(e *assistant.MessageEvent) { e.Platform = assistant.PlatformTelegram }} {
		event := trustedEvent()
		mutate(&event)
		if h.ConsumePrivateMessage(context.Background(), event, command) {
			t.Fatal("untrusted sender confirmed")
		}
	}
	if h.ConsumePrivateMessage(context.Background(), trustedEvent(), b["code"].(string)) {
		t.Fatal("bare code confirmed external login")
	}
	runtime.cfg.OwnerLoginEnabled = false
	if h.ConsumePrivateMessage(context.Background(), trustedEvent(), command) {
		t.Fatal("disabled profile confirmed")
	}
	runtime.cfg.OwnerLoginEnabled = true
	key := hashOwnerPairingToken(b["poll_token"].(string))
	h.pairs[key].expires = time.Now().Add(-time.Second)
	if h.ConsumePrivateMessage(context.Background(), trustedEvent(), command) {
		t.Fatal("expired code confirmed")
	}
	rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair/status", `{"poll_token":"`+b["poll_token"].(string)+`"}`)
	if !strings.Contains(rec.Body.String(), `"expired":true`) {
		t.Fatal(rec.Body.String())
	}
}
func TestTrustedLoginLimitsAndCredentials(t *testing.T) {
	r, h, _, _ := trustedFixture(t)
	b := trustedCreate(t, r)
	if rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair", ""); rec.Code != 429 {
		t.Fatal("create rate limit", rec.Code)
	}
	body := `{"poll_token":"` + b["poll_token"].(string) + `"}`
	if rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair/status", body); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"approved":false`) {
		t.Fatal("pending result", rec.Body)
	}
	if rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair/status", body); rec.Code != 429 {
		t.Fatal("poll limit", rec.Code)
	}
	for i := 0; i < 8; i++ {
		rec := trustedRequest(r, "missing", "/api/auth/apps/pair", "")
		if i < 6 && rec.Code != 401 {
			t.Fatal(rec.Code)
		}
		if i == 7 && rec.Code != 429 {
			t.Fatal("auth throttle", rec.Code)
		}
	}
	if len(h.pairs) != 1 {
		t.Fatal("invalid requests allocated pairings")
	}
}
func TestTrustedLoginConfig(t *testing.T) {
	if apps, err := LoadTrustedLoginApps(""); err != nil || len(apps) != 0 {
		t.Fatal("not disabled by default")
	}
	p := filepath.Join(t.TempDir(), "apps.json")
	for _, s := range []string{`[{"id":"x"}]`, `[] {}`, `[{"unknown":true}]`} {
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTrustedLoginApps(p); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	r, h, _, _ := trustedFixture(t)
	delete(h.apps, "ruhua")
	if rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair", ""); rec.Code != 401 {
		t.Fatal("disabled app accepted")
	}
}

func TestTrustedLoginCapacityAndConsoleIsolation(t *testing.T) {
	r, h, owner, _ := trustedFixture(t)
	// Console-created tokens and codes cannot be used as application pairings.
	code, token := createOwnerPairing(t, r)
	if h.ConsumePrivateMessage(context.Background(), trustedEvent(), "登录应用 ruhua "+code) {
		t.Fatal("console code became external identity")
	}
	rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair/status", `{"poll_token":"`+token+`"}`)
	if !strings.Contains(rec.Body.String(), `"expired":true`) {
		t.Fatal("console token readable externally")
	}
	if !owner.ConsumePrivateMessage(context.Background(), trustedEvent(), code) {
		t.Fatal("console flow broken")
	}
	for i := 0; i < 8; i++ {
		h.lastCreate["ruhua"] = time.Time{}
		trustedCreate(t, r)
	}
	h.lastCreate["ruhua"] = time.Time{}
	if rec := trustedRequest(r, "ruhua", "/api/auth/apps/pair", ""); rec.Code != 429 {
		t.Fatal("capacity exceeded", rec.Code)
	}
}
