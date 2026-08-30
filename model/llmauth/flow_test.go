// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llmauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func testProvider(tokenURL string) Provider {
	return Provider{
		Key:          "example",
		Label:        "示例服务",
		AuthorizeURL: "https://auth.example.invalid/authorize",
		TokenURL:     tokenURL,
		ClientID:     "diana-console",
		RedirectURI:  "https://console.example.invalid/callback",
		Scopes:       []string{"models.read", "models.write"},
		UsePKCE:      true,
	}
}

// 授权地址必须带齐 PKCE 参数。少了 code_challenge，授权码在重定向里就是裸的，
// 谁截到谁就能换走令牌。
func TestFlowStartBuildsPKCEAuthorizeURL(t *testing.T) {
	flow := NewFlow(http.DefaultClient)
	login, err := flow.Start(testProvider("https://token.example.invalid/token"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	parsed, err := url.Parse(login.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorize url = %q: %v", login.AuthorizeURL, err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "diana-console",
		"redirect_uri":          "https://console.example.invalid/callback",
		"scope":                 "models.read models.write",
		"code_challenge_method": "S256",
		"state":                 login.State,
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("authorize url %s = %q, want %q", key, got, want)
		}
	}
	sum := sha256.Sum256([]byte(login.CodeVerifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); query.Get("code_challenge") != want {
		t.Fatalf("code_challenge = %q, want the S256 hash of the verifier", query.Get("code_challenge"))
	}
	// verifier 只留在服务端，不能出现在给用户的地址里。
	if strings.Contains(login.AuthorizeURL, login.CodeVerifier) {
		t.Fatal("code verifier leaked into the authorize URL")
	}
}

// 用户从地址栏复制回来的是整条回调地址，不该要求他自己从里面挑出 code。
func TestParseCallbackAcceptsWhatUsersActuallyPaste(t *testing.T) {
	cases := []struct {
		name  string
		input string
		code  string
		state string
	}{
		{"整条回调地址", "https://console.example.invalid/callback?code=abc123&state=xyz", "abc123", "xyz"},
		{"裸授权码", "abc123", "abc123", ""},
		{"前后有空白", "  https://console.example.invalid/callback?code=abc123&state=xyz  ", "abc123", "xyz"},
		{"结果放在锚点里", "https://console.example.invalid/callback#code=abc123&state=xyz", "abc123", "xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := parseCallback(tc.input)
			if err != nil {
				t.Fatalf("parseCallback(%q) error = %v", tc.input, err)
			}
			if code != tc.code || state != tc.state {
				t.Fatalf("parseCallback(%q) = %q, %q, want %q, %q", tc.input, code, state, tc.code, tc.state)
			}
		})
	}
}

// 授权页把失败也回调回来。当成成功往下走的话，用户看到的是「没有 code」这种
// 无从下手的报错，而不是提供商给出的真实原因。
func TestParseCallbackSurfacesProviderErrors(t *testing.T) {
	_, _, err := parseCallback("https://console.example.invalid/callback?error=access_denied&error_description=用户取消了授权")
	if err == nil || !strings.Contains(err.Error(), "用户取消了授权") {
		t.Fatalf("parseCallback() error = %v, want the provider's own reason", err)
	}
}

// state 对不上就是跨站请求伪造的信号，必须拒绝。
func TestFlowCompleteRejectsMismatchedState(t *testing.T) {
	flow := NewFlow(http.DefaultClient)
	provider := testProvider("https://token.example.invalid/token")
	login, err := flow.Start(provider)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, err = flow.Complete(context.Background(), provider, login.ID,
		"https://console.example.invalid/callback?code=abc123&state=forged")
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("Complete() error = %v, want a state mismatch refusal", err)
	}
}

func TestFlowCompleteRejectsExpiredLogin(t *testing.T) {
	flow := NewFlow(http.DefaultClient)
	provider := testProvider("https://token.example.invalid/token")
	login, err := flow.Start(provider)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	flow.now = func() time.Time { return login.ExpiresAt.Add(time.Second) }
	if _, err := flow.Complete(context.Background(), provider, login.ID,
		"https://console.example.invalid/callback?code=abc&state="+login.State); err == nil {
		t.Fatal("an expired login was accepted")
	}
	// 过期的那次授权连同它的 verifier 一起清掉，不留在内存里。
	if _, ok := flow.Pending(login.ID); ok {
		t.Fatal("expired login was kept in memory")
	}
}

// tokenTestServer 记录换令牌请求并按脚本回应。
type tokenTestServer struct {
	*httptest.Server
	requests []map[string]string
}

func newTokenTestServer(t *testing.T, respond func(form map[string]string) (int, string)) *tokenTestServer {
	t.Helper()
	server := &tokenTestServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		server.requests = append(server.requests, body)
		status, payload := respond(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFlowCompleteExchangesTheCodeWithTheVerifier(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":3600,"scope":"models.read"}`
	})
	flow := NewFlow(server.Client())
	provider := testProvider(server.URL)
	provider.AuthorizeURL = "https://auth.example.invalid/authorize"
	login, err := flow.Start(provider)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	token, err := flow.Complete(context.Background(), provider, login.ID,
		"https://console.example.invalid/callback?code=abc123&state="+url.QueryEscape(login.State))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if token.AccessToken != "at-1" || token.RefreshToken != "rt-1" || token.ProviderKey != "example" {
		t.Fatalf("token = %#v", token)
	}
	if token.ExpiresAt.IsZero() {
		t.Fatal("expires_in was ignored, so the token will never be refreshed")
	}
	if len(server.requests) != 1 {
		t.Fatalf("token requests = %d", len(server.requests))
	}
	sent := server.requests[0]
	if sent["grant_type"] != "authorization_code" || sent["code"] != "abc123" || sent["code_verifier"] != login.CodeVerifier {
		t.Fatalf("token request = %#v", sent)
	}

	// 换完就作废，同一个授权码不能再用第二次。
	if _, ok := flow.Pending(login.ID); ok {
		t.Fatal("completed login was left pending")
	}
}

// OpenRouter 换回来的字段叫 key 而不是 access_token，而且不会自己过期。
func TestFlowCompleteAcceptsAKeyStyleResponse(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"key":"sk-or-v1-example"}`
	})
	flow := NewFlow(server.Client())
	provider := testProvider(server.URL)
	login, _ := flow.Start(provider)
	token, err := flow.Complete(context.Background(), provider, login.ID, "code-only")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if token.AccessToken != "sk-or-v1-example" {
		t.Fatalf("token = %#v", token)
	}
	if !token.ExpiresAt.IsZero() || token.Expired(time.Now()) {
		t.Fatalf("a non-expiring key was marked as expiring: %#v", token)
	}
}

// 提供商拒绝时要把它自己的说明带出来，而不是回一句「HTTP 400」。
func TestFlowCompleteSurfacesTokenEndpointErrors(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"授权码已使用"}`
	})
	flow := NewFlow(server.Client())
	provider := testProvider(server.URL)
	login, _ := flow.Start(provider)
	_, err := flow.Complete(context.Background(), provider, login.ID, "code-only")
	if err == nil || !strings.Contains(err.Error(), "授权码已使用") {
		t.Fatalf("Complete() error = %v", err)
	}
}

// 令牌响应里有凭据，报错信息不能把整段响应体回显出去。
func TestFlowDoesNotEchoTokenResponseBodies(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `not json at all, access_token=leaked-secret`
	})
	flow := NewFlow(server.Client())
	provider := testProvider(server.URL)
	login, _ := flow.Start(provider)
	_, err := flow.Complete(context.Background(), provider, login.ID, "code-only")
	if err == nil {
		t.Fatal("a malformed token response was accepted")
	}
	if strings.Contains(err.Error(), "leaked-secret") {
		t.Fatalf("token response body leaked into the error: %v", err)
	}
}

// 有的提供商续期时不再下发 refresh token，意思是旧的继续用。
// 不接住这一点，第二次续期就会因为「没有续期令牌」而要求重新登录。
func TestFlowRefreshKeepsTheOldRefreshTokenWhenOmitted(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-2","expires_in":3600}`
	})
	flow := NewFlow(server.Client())
	provider := testProvider(server.URL)
	refreshed, err := flow.Refresh(context.Background(), provider, Token{
		ProviderKey: "example", AccessToken: "at-1", RefreshToken: "rt-1", Account: "someone",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.AccessToken != "at-2" || refreshed.RefreshToken != "rt-1" {
		t.Fatalf("refreshed = %#v", refreshed)
	}
	if refreshed.Account != "someone" {
		t.Fatalf("refresh dropped the account label: %#v", refreshed)
	}
	if sent := server.requests[0]; sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "rt-1" {
		t.Fatalf("refresh request = %#v", sent)
	}
}

// 提前一点续期。卡着过期时间才换，请求正在路上时就已经失效了。
func TestTokenExpiryLeavesRoomToRefresh(t *testing.T) {
	now := time.Now()
	token := Token{AccessToken: "at", ExpiresAt: now.Add(refreshSkew / 2)}
	if !token.Expired(now) {
		t.Fatal("a token expiring inside the refresh window was treated as fresh")
	}
	if token.Valid(now) {
		t.Fatal("Valid() disagreed with Expired()")
	}
	if fresh := (Token{AccessToken: "at", ExpiresAt: now.Add(time.Hour)}); !fresh.Valid(now) {
		t.Fatal("a token valid for an hour was treated as expired")
	}
	// 不会过期的令牌永远有效。
	if forever := (Token{AccessToken: "at"}); !forever.Valid(now) || forever.Expired(now) {
		t.Fatal("a non-expiring token was treated as expired")
	}
}

// 明文地址会把授权码和令牌裸发出去，而这个地址是用户填的。
func TestProviderNormalizeRejectsPlaintextEndpoints(t *testing.T) {
	_, err := Provider{Key: "x", AuthorizeURL: "http://auth.example.invalid/a", TokenURL: "https://t.example.invalid/t"}.Normalize()
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("Normalize() error = %v, want an https requirement", err)
	}
	// 没有 client secret 的公共客户端一律强制 PKCE。
	normalized, err := Provider{
		Key: "x", AuthorizeURL: "https://auth.example.invalid/a", TokenURL: "https://t.example.invalid/t", UsePKCE: false,
	}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !normalized.UsePKCE {
		t.Fatal("a public client was allowed to skip PKCE")
	}
}

func TestTokenRedactedDropsSecrets(t *testing.T) {
	token := Token{AccessToken: "at", RefreshToken: "rt", Account: "someone", Scope: "models.read"}
	redacted := token.Redacted()
	if redacted.AccessToken != "" || redacted.RefreshToken != "" {
		t.Fatalf("Redacted() = %#v", redacted)
	}
	if redacted.Account != "someone" || redacted.Scope != "models.read" {
		t.Fatalf("Redacted() dropped the harmless fields too: %#v", redacted)
	}
}

// 凭据是 llm 那边要的形状：Bearer + 摘掉 API Key 头。
func TestManagerCredentialShape(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-1","expires_in":3600}`
	})
	manager := NewManager(nil, server.Client())
	provider := testProvider(server.URL)
	provider.TokenHeaders = map[string]string{"X-Example-Beta": "on"}
	if _, err := manager.SaveCustomProvider(context.Background(), provider); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, err := manager.StartLogin("example")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	credential, err := manager.Credential(context.Background(), "example")
	if err != nil {
		t.Fatalf("Credential() error = %v", err)
	}
	name, value := credential.AuthHeader()
	if credential.Kind != llm.CredentialKindOAuth || name != "Authorization" || value != "Bearer at-1" {
		t.Fatalf("credential = %#v (%s: %s)", credential, name, value)
	}
	if !credential.ReplaceProviderAuth {
		t.Fatal("OAuth credential did not ask to drop the provider's own auth header")
	}
	if credential.Headers["X-Example-Beta"] != "on" {
		t.Fatalf("provider headers were dropped: %#v", credential.Headers)
	}
}

// 过期就地续期，不靠后台定时任务。
func TestManagerRefreshesExpiredTokensOnUse(t *testing.T) {
	exchanges := 0
	server := newTokenTestServer(t, func(form map[string]string) (int, string) {
		exchanges++
		if form["grant_type"] == "refresh_token" {
			return http.StatusOK, `{"access_token":"at-2","expires_in":3600}`
		}
		return http.StatusOK, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`
	})
	store := &memoryAuthStore{}
	manager := NewManager(store, server.Client())
	if _, err := manager.SaveCustomProvider(context.Background(), testProvider(server.URL)); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, _ := manager.StartLogin("example")
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	// 把时间推到过期之后。
	manager.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	credential, err := manager.Credential(context.Background(), "example")
	if err != nil {
		t.Fatalf("Credential() error = %v", err)
	}
	if credential.Token != "at-2" {
		t.Fatalf("credential.Token = %q, want the refreshed token", credential.Token)
	}
	if exchanges != 2 {
		t.Fatalf("token endpoint calls = %d, want the exchange plus one refresh", exchanges)
	}
	// 续期结果要落库，否则重启后又退回过期的那把。
	if len(store.doc.Tokens) != 1 || store.doc.Tokens[0].AccessToken != "at-2" {
		t.Fatalf("refreshed token was not persisted: %#v", store.doc.Tokens)
	}
}

// 登录状态给控制台看，但令牌本身一个字节都不能出去。
func TestManagerStatusesNeverExposeTokens(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"super-secret","refresh_token":"also-secret","expires_in":3600}`
	})
	manager := NewManager(nil, server.Client())
	provider := testProvider(server.URL)
	provider.ClientSecret = "client-secret-value"
	provider.UsePKCE = true
	if _, err := manager.SaveCustomProvider(context.Background(), provider); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, _ := manager.StartLogin("example")
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	encoded, err := json.Marshal(manager.Statuses())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, secret := range []string{"super-secret", "also-secret", "client-secret-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status payload leaked %q: %s", secret, encoded)
		}
	}
	var statuses []Status
	if err := json.Unmarshal(encoded, &statuses); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	found := false
	for _, status := range statuses {
		if status.Provider.Key == "example" {
			found = true
			if !status.LoggedIn || status.ExpiresAt == nil {
				t.Fatalf("status = %#v", status)
			}
		}
	}
	if !found {
		t.Fatal("the custom provider is missing from the statuses")
	}
}

// 自定义提供商不能顶掉内置的，否则「内置地址可信」这个前提就没了。
func TestManagerRefusesToShadowBuiltinProviders(t *testing.T) {
	manager := NewManager(nil, http.DefaultClient)
	builtin := builtinProviders()[0]
	_, err := manager.SaveCustomProvider(context.Background(), Provider{
		Key: builtin.Key, AuthorizeURL: "https://evil.example.invalid/a", TokenURL: "https://evil.example.invalid/t",
	})
	if err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("SaveCustomProvider() error = %v, want a refusal", err)
	}
}

// 改了地址或 client ID 就等于换了一家服务，旧令牌不能跟着走。
func TestManagerDropsTokensWhenAProviderIsRepointed(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-1"}`
	})
	manager := NewManager(nil, server.Client())
	provider := testProvider(server.URL)
	if _, err := manager.SaveCustomProvider(context.Background(), provider); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, _ := manager.StartLogin("example")
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if !manager.HasToken("example") {
		t.Fatal("login did not stick")
	}
	moved := provider
	moved.TokenURL = "https://elsewhere.example.invalid/token"
	if _, err := manager.SaveCustomProvider(context.Background(), moved); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	if manager.HasToken("example") {
		t.Fatal("the old token survived being pointed at a different service")
	}
}

type memoryAuthStore struct{ doc Document }

func (s *memoryAuthStore) LoadAuth(context.Context) (Document, error) { return s.doc, nil }
func (s *memoryAuthStore) SaveAuth(_ context.Context, doc Document) error {
	s.doc = doc
	return nil
}

// 存进去的状态要能原样读回来，包括自定义提供商和令牌。
func TestManagerRestoreRoundTrip(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-1","refresh_token":"rt-1"}`
	})
	store := &memoryAuthStore{}
	manager := NewManager(store, server.Client())
	if _, err := manager.SaveCustomProvider(context.Background(), testProvider(server.URL)); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, _ := manager.StartLogin("example")
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}

	restored := NewManager(store, server.Client())
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !restored.HasToken("example") {
		t.Fatal("token did not survive a restart")
	}
	if _, ok := restored.Provider("example"); !ok {
		t.Fatal("custom provider did not survive a restart")
	}
	// 一条坏数据不该让整页打不开。
	store.doc.CustomProviders = append(store.doc.CustomProviders, Provider{Key: "broken", AuthorizeURL: "not a url"})
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore() with one bad row error = %v", err)
	}
	if _, ok := restored.Provider("example"); !ok {
		t.Fatal("a bad row took the good ones down with it")
	}
	if _, ok := restored.Provider("broken"); ok {
		t.Fatal("an invalid provider was restored")
	}
}

func TestManagerLogoutClearsTheToken(t *testing.T) {
	server := newTokenTestServer(t, func(map[string]string) (int, string) {
		return http.StatusOK, `{"access_token":"at-1"}`
	})
	store := &memoryAuthStore{}
	manager := NewManager(store, server.Client())
	if _, err := manager.SaveCustomProvider(context.Background(), testProvider(server.URL)); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, _ := manager.StartLogin("example")
	if _, err := manager.CompleteLogin(context.Background(), "example", login.ID, "code-only"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if err := manager.Logout(context.Background(), "example"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if manager.HasToken("example") {
		t.Fatal("logout left the token behind")
	}
	if len(store.doc.Tokens) != 0 {
		t.Fatalf("logout did not reach storage: %#v", store.doc.Tokens)
	}
	if err := manager.Logout(context.Background(), "example"); err == nil {
		t.Fatal("logging out twice should say there is nothing to log out of")
	}
	_ = fmt.Sprint()
}
