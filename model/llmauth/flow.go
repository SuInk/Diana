// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llmauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 授权流程本身：PKCE、换令牌、续期。
//
// 控制台跑在服务器上，浏览器未必和它同一台机器，所以回调不强求能落回本机——
// 用户在授权页点完同意后，把浏览器地址栏里的整条回调地址（或其中的 code）
// 粘回控制台即可。这也是 Pi 在远程机器上的做法，对 WebUI 来说它是主路径而不是兜底。

const (
	// pendingLoginTTL 是一次授权的有效期。授权页开着不动的情况很常见，
	// 给够时间；但不能不过期——待完成的授权里存着 verifier。
	pendingLoginTTL = 15 * time.Minute
	// refreshSkew 是提前续期的余量。卡着过期时间续期，会在请求正在路上时失效。
	// 五分钟是这类客户端的常见取值：一轮长对话里工具调用能连着跑好几分钟，
	// 余量小于这个跨度，就会有请求在半路上撞到过期。
	refreshSkew = 5 * time.Minute

	tokenRequestTimeout = 30 * time.Second
)

// PendingLogin 是一次已经发起、还没完成的授权。
type PendingLogin struct {
	ID           string    `json:"id"`
	ProviderKey  string    `json:"provider_key"`
	State        string    `json:"state"`
	CodeVerifier string    `json:"-"`
	AuthorizeURL string    `json:"authorize_url"`
	RedirectURI  string    `json:"redirect_uri,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Token 是一次授权换到的凭据。
type Token struct {
	ProviderKey  string `json:"provider_key"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// ExpiresAt 为零表示这把令牌不会自己过期（OpenRouter 换到的 Key 就是这样）。
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	ObtainedAt time.Time `json:"obtained_at"`
	// Account 是可显示的账号标识，仅用于控制台上让人认出登录的是哪个号。
	Account string `json:"account,omitempty"`
}

// Expired 判断令牌是不是已经（或即将）过期。
func (t Token) Expired(now time.Time) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return !now.Add(refreshSkew).Before(t.ExpiresAt)
}

// Valid 判断这把令牌现在能不能直接用。
func (t Token) Valid(now time.Time) bool {
	return strings.TrimSpace(t.AccessToken) != "" && !t.Expired(now)
}

// Refreshable 判断过期后还能不能自己救回来。
func (t Token) Refreshable() bool {
	return strings.TrimSpace(t.RefreshToken) != ""
}

// Redacted 返回可以安全交给控制台的副本：令牌本身永远不出现在读接口里。
func (t Token) Redacted() Token {
	t.AccessToken = ""
	t.RefreshToken = ""
	return t
}

// Flow 执行授权流程。
type Flow struct {
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	pending map[string]PendingLogin
}

func NewFlow(client *http.Client) *Flow {
	return &Flow{client: client, now: time.Now, pending: map[string]PendingLogin{}}
}

// tokenClient 挑发换令牌请求用的客户端。
//
// 令牌地址虽然是管理员填的，但换令牌是服务端主动发起的出网请求，公网目标一律
// 走 netguard，免得这个输入框变成探内网的入口。本机回环是管理员明确指向自建
// 网关的情况，那正是 netguard 会拦下的地址，所以单独放行。
func (f *Flow) tokenClient(tokenURL string) *http.Client {
	if f.client != nil {
		return f.client
	}
	if parsed, err := url.Parse(tokenURL); err == nil && isLoopbackHost(parsed.Hostname()) {
		return &http.Client{Timeout: tokenRequestTimeout}
	}
	return netguard.NewPublicHTTPClient(tokenRequestTimeout)
}

// Start 发起一次授权，返回要让用户打开的地址。
func (f *Flow) Start(provider Provider) (PendingLogin, error) {
	provider, err := provider.Normalize()
	if err != nil {
		return PendingLogin{}, err
	}
	state, err := randomToken(24)
	if err != nil {
		return PendingLogin{}, err
	}
	id, err := randomToken(16)
	if err != nil {
		return PendingLogin{}, err
	}
	verifier := ""
	if provider.UsePKCE {
		if verifier, err = randomToken(48); err != nil {
			return PendingLogin{}, err
		}
	}
	authorizeURL, err := buildAuthorizeURL(provider, state, verifier)
	if err != nil {
		return PendingLogin{}, err
	}
	now := f.now()
	login := PendingLogin{
		ID:           id,
		ProviderKey:  provider.Key,
		State:        state,
		CodeVerifier: verifier,
		AuthorizeURL: authorizeURL,
		RedirectURI:  provider.RedirectURI,
		CreatedAt:    now,
		ExpiresAt:    now.Add(pendingLoginTTL),
	}
	f.mu.Lock()
	f.pruneExpiredLocked(now)
	f.pending[id] = login
	f.mu.Unlock()
	return login, nil
}

// Pending 取一次待完成的授权。
func (f *Flow) Pending(id string) (PendingLogin, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneExpiredLocked(f.now())
	login, ok := f.pending[strings.TrimSpace(id)]
	return login, ok
}

// Cancel 放弃一次待完成的授权。
func (f *Flow) Cancel(id string) {
	f.mu.Lock()
	delete(f.pending, strings.TrimSpace(id))
	f.mu.Unlock()
}

func (f *Flow) pruneExpiredLocked(now time.Time) {
	for id, login := range f.pending {
		if now.After(login.ExpiresAt) {
			delete(f.pending, id)
		}
	}
}

// Complete 用用户粘回来的内容换取令牌。
//
// 参数可以是整条回调地址，也可以只是其中的授权码——用户从浏览器地址栏复制的
// 就是前者，让他自己从一长串查询参数里挑出 code 才是多余的要求。
func (f *Flow) Complete(ctx context.Context, provider Provider, loginID, pasted string) (Token, error) {
	provider, err := provider.Normalize()
	if err != nil {
		return Token{}, err
	}
	f.mu.Lock()
	login, ok := f.pending[strings.TrimSpace(loginID)]
	f.mu.Unlock()
	if !ok {
		return Token{}, fmt.Errorf("llmauth: 这次授权已过期或已完成，请重新发起")
	}
	if login.ProviderKey != provider.Key {
		return Token{}, fmt.Errorf("llmauth: 授权与提供商不匹配")
	}
	if f.now().After(login.ExpiresAt) {
		f.Cancel(login.ID)
		return Token{}, fmt.Errorf("llmauth: 这次授权已过期，请重新发起")
	}
	code, state, err := parseCallback(pasted)
	if err != nil {
		return Token{}, err
	}
	// state 用定时安全比较：它是防跨站请求伪造的那道锁，别在比较上泄露信息。
	if state != "" && subtle.ConstantTimeCompare([]byte(state), []byte(login.State)) != 1 {
		return Token{}, fmt.Errorf("llmauth: 回调的 state 与本次授权不一致，请重新发起")
	}
	token, err := f.exchange(ctx, provider, login, code)
	if err != nil {
		return Token{}, err
	}
	f.Cancel(login.ID)
	return token, nil
}

// tokenResponse 覆盖 RFC 6749 的标准字段。expires_in 各家有发数字也有发字符串的，
// 用 json.Number 一并收下。
type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	TokenType    string      `json:"token_type"`
	Scope        string      `json:"scope"`
	ExpiresIn    json.Number `json:"expires_in"`
	// Key 是 OpenRouter 的写法：它换给你的不是 access token，而是一把用户 Key。
	Key              string `json:"key"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (f *Flow) exchange(ctx context.Context, provider Provider, login PendingLogin, code string) (Token, error) {
	payload := map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
		"client_id":  provider.ClientID,
	}
	if provider.RedirectURI != "" {
		payload["redirect_uri"] = provider.RedirectURI
	}
	if provider.ClientSecret != "" {
		payload["client_secret"] = provider.ClientSecret
	}
	if login.CodeVerifier != "" {
		payload["code_verifier"] = login.CodeVerifier
	}
	return f.postToken(ctx, provider, payload)
}

// Refresh 用 refresh token 换一把新的。
func (f *Flow) Refresh(ctx context.Context, provider Provider, token Token) (Token, error) {
	provider, err := provider.Normalize()
	if err != nil {
		return Token{}, err
	}
	if !token.Refreshable() {
		return Token{}, fmt.Errorf("llmauth: 这份凭据没有续期令牌，需要重新登录")
	}
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": token.RefreshToken,
		"client_id":     provider.ClientID,
	}
	if provider.ClientSecret != "" {
		payload["client_secret"] = provider.ClientSecret
	}
	refreshed, err := f.postToken(ctx, provider, payload)
	if err != nil {
		return Token{}, err
	}
	// 有的提供商续期时不再下发 refresh token，意思是旧的继续用。
	// 不接住这一点，第二次续期就会因为「没有续期令牌」而要求重新登录。
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	if refreshed.Account == "" {
		refreshed.Account = token.Account
	}
	return refreshed, nil
}

// encodeTokenRequest 按提供商声明的格式打包换令牌的请求体。
//
// 响应无论哪种格式都按 JSON 解析：RFC 6749 §5.1 规定令牌响应就是 JSON，
// 这一头没有方言。
func encodeTokenRequest(format TokenRequestFormat, payload map[string]string) (string, string, error) {
	if format == TokenRequestJSON {
		body, err := json.Marshal(payload)
		if err != nil {
			return "", "", err
		}
		return string(body), "application/json", nil
	}
	values := make(url.Values, len(payload))
	for name, value := range payload {
		values.Set(name, value)
	}
	return values.Encode(), "application/x-www-form-urlencoded", nil
}

func (f *Flow) postToken(ctx context.Context, provider Provider, payload map[string]string) (Token, error) {
	body, contentType, err := encodeTokenRequest(provider.TokenRequestFormat, payload)
	if err != nil {
		return Token{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, tokenRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, provider.TokenURL, strings.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := f.tokenClient(provider.TokenURL).Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("llmauth: 请求令牌失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("llmauth: 读取令牌响应失败: %w", err)
	}
	var parsed tokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 响应体可能含令牌，不能整段回显给用户或写进日志。
		return Token{}, fmt.Errorf("llmauth: 令牌响应不是有效 JSON（HTTP %d）", resp.StatusCode)
	}
	if parsed.Error != "" {
		detail := strings.TrimSpace(parsed.ErrorDescription)
		if detail == "" {
			detail = parsed.Error
		}
		return Token{}, fmt.Errorf("llmauth: 授权被拒绝：%s", detail)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Token{}, fmt.Errorf("llmauth: 令牌接口返回 HTTP %d", resp.StatusCode)
	}
	access := strings.TrimSpace(parsed.AccessToken)
	if access == "" {
		// OpenRouter 那种「换一把用户 Key」的写法。
		access = strings.TrimSpace(parsed.Key)
	}
	if access == "" {
		return Token{}, fmt.Errorf("llmauth: 令牌响应里没有可用的凭据")
	}
	now := f.now()
	token := Token{
		ProviderKey:  provider.Key,
		AccessToken:  access,
		RefreshToken: strings.TrimSpace(parsed.RefreshToken),
		TokenType:    strings.TrimSpace(parsed.TokenType),
		Scope:        strings.TrimSpace(parsed.Scope),
		ObtainedAt:   now,
	}
	if seconds, err := parsed.ExpiresIn.Int64(); err == nil && seconds > 0 {
		token.ExpiresAt = now.Add(time.Duration(seconds) * time.Second)
	}
	return token, nil
}

func buildAuthorizeURL(provider Provider, state, verifier string) (string, error) {
	parsed, err := url.Parse(provider.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("llmauth: 授权地址无效: %w", err)
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("state", state)
	if provider.ClientID != "" {
		query.Set("client_id", provider.ClientID)
	}
	if provider.RedirectURI != "" {
		query.Set("redirect_uri", provider.RedirectURI)
	}
	if len(provider.Scopes) > 0 {
		query.Set("scope", strings.Join(provider.Scopes, " "))
	}
	if verifier != "" {
		query.Set("code_challenge", pkceChallenge(verifier))
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// parseCallback 从用户粘回来的内容里取出授权码。
func parseCallback(pasted string) (code string, state string, err error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return "", "", fmt.Errorf("llmauth: 请粘贴授权后的回调地址或授权码")
	}
	if !strings.Contains(pasted, "://") && !strings.Contains(pasted, "?") && !strings.Contains(pasted, "&") {
		// 就是一段裸的授权码。
		return pasted, "", nil
	}
	parsed, err := url.Parse(pasted)
	if err != nil {
		return "", "", fmt.Errorf("llmauth: 回调地址无法解析，可以只粘贴其中的 code")
	}
	query := parsed.Query()
	if len(query) == 0 && parsed.Fragment != "" {
		// 有的提供商把结果放在 # 后面。
		if fragmentQuery, fragmentErr := url.ParseQuery(parsed.Fragment); fragmentErr == nil {
			query = fragmentQuery
		}
	}
	if failure := strings.TrimSpace(query.Get("error")); failure != "" {
		detail := strings.TrimSpace(query.Get("error_description"))
		if detail == "" {
			detail = failure
		}
		return "", "", fmt.Errorf("llmauth: 授权未通过：%s", detail)
	}
	code = strings.TrimSpace(query.Get("code"))
	if code == "" {
		return "", "", fmt.Errorf("llmauth: 回调地址里没有 code")
	}
	return code, strings.TrimSpace(query.Get("state")), nil
}

// pkceChallenge 按 RFC 7636 的 S256 方法计算 challenge。
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("llmauth: 生成随机串失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
