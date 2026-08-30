// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordingTransport 记下最终发出去的请求头。
type recordingTransport struct{ last http.Header }

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.last = req.Header.Clone()
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func doThroughCredentials(t *testing.T, source CredentialSource, seed func(*http.Request)) http.Header {
	t.Helper()
	recorder := &recordingTransport{}
	client := httpClientWithCredentials(&http.Client{Transport: recorder}, source)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.invalid/v1/x", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if seed != nil {
		seed(req)
	}
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return recorder.last
}

// OAuth 凭据要换上 Bearer，并摘掉 SDK 自己写的 API Key 头——同时带两种鉴权头的
// 请求会被一部分网关直接拒掉，而那种报错通常不会说是因为头重复。
func TestCredentialTransportSwapsAPIKeyForBearer(t *testing.T) {
	source := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{
			Kind:                CredentialKindOAuth,
			Token:               "oauth-token",
			Headers:             map[string]string{"Anthropic-Beta": "oauth"},
			ReplaceProviderAuth: true,
		}, nil
	})
	header := doThroughCredentials(t, source, func(req *http.Request) {
		req.Header.Set("X-Api-Key", "sk-configured")
		req.Header.Set("X-Goog-Api-Key", "goog-configured")
	})
	if got := header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if header.Get("X-Api-Key") != "" || header.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("api key headers survived alongside the bearer token: %#v", header)
	}
	if header.Get("Anthropic-Beta") != "oauth" {
		t.Fatalf("provider headers were dropped: %#v", header)
	}
}

// 只配了 API Key 的配置档一个字节都不该被改动。
func TestCredentialTransportLeavesAPIKeyRequestsAlone(t *testing.T) {
	source := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{Kind: CredentialKindAPIKey, Token: "sk-configured"}, nil
	})
	header := doThroughCredentials(t, source, func(req *http.Request) {
		req.Header.Set("X-Api-Key", "sk-configured")
	})
	if header.Get("X-Api-Key") != "sk-configured" {
		t.Fatalf("api key header was disturbed: %#v", header)
	}
	if header.Get("Authorization") != "" {
		t.Fatalf("an API key config grew an Authorization header: %#v", header)
	}
}

// 续期失败时请求照发，让配置里的 API Key 继续兜底。集体 401 比降级严重得多。
func TestCredentialTransportFallsThroughWhenResolutionFails(t *testing.T) {
	source := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{}, errors.New("refresh failed")
	})
	header := doThroughCredentials(t, source, func(req *http.Request) {
		req.Header.Set("X-Api-Key", "sk-configured")
	})
	if header.Get("X-Api-Key") != "sk-configured" {
		t.Fatalf("the fallback API key was stripped: %#v", header)
	}
}

// RoundTripper 不允许改传入的请求。改了的话重试会带上上一次的头。
func TestCredentialTransportDoesNotMutateTheIncomingRequest(t *testing.T) {
	source := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{Kind: CredentialKindOAuth, Token: "oauth-token", ReplaceProviderAuth: true}, nil
	})
	recorder := &recordingTransport{}
	client := httpClientWithCredentials(&http.Client{Transport: recorder}, source)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.invalid/v1/x", nil)
	req.Header.Set("X-Api-Key", "sk-configured")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if req.Header.Get("X-Api-Key") != "sk-configured" || req.Header.Get("Authorization") != "" {
		t.Fatalf("the caller's request was mutated: %#v", req.Header)
	}
}

// 没有凭据来源时连包装都不该发生，行为与改动前完全一致。
func TestHTTPClientWithoutCredentialsIsUntouched(t *testing.T) {
	client := &http.Client{}
	if got := httpClientWithCredentials(client, nil); got != client {
		t.Fatal("a client without a credential source was still wrapped")
	}
}

// 端到端：NewClient 注入的凭据要真的落到出站请求上。
func TestNewClientAppliesTheCredentialSource(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"response","output":[],"usage":{}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "sk-configured",
		BaseURL:  server.URL,
		Model:    "gpt-test",
	}, WithHTTPClient(server.Client()), WithCredentialSource(CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{Kind: CredentialKindOAuth, Token: "oauth-token", ReplaceProviderAuth: true}, nil
	})))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, _ = client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if seen != "Bearer oauth-token" {
		t.Fatalf("outbound Authorization = %q, want the OAuth token", seen)
	}
}

// 缓存层保证同一轮对话里的多次请求只解析一次，Invalidate 之后才重新解析。
func TestCachedCredentialSourceResolvesOncePerValidity(t *testing.T) {
	calls := 0
	cached := newCachedCredentialSource(CredentialSourceFunc(func(context.Context) (Credential, error) {
		calls++
		return Credential{Kind: CredentialKindOAuth, Token: "t"}, nil
	}))
	for i := 0; i < 3; i++ {
		if _, err := cached.Credential(context.Background()); err != nil {
			t.Fatalf("Credential() error = %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("resolutions = %d, want 1", calls)
	}
	cached.Invalidate()
	if _, err := cached.Credential(context.Background()); err != nil {
		t.Fatalf("Credential() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("resolutions after Invalidate = %d, want 2", calls)
	}
}

// 「OAuth 令牌一定走 Authorization: Bearer」是错的：Anthropic 对 Bearer 里的
// OAuth 令牌回 401，要求换成 x-api-key 且不带前缀。凭据能指定落点，并且要把
// SDK 自己写上的另一个鉴权头摘掉。
func TestCredentialTransportHonoursACustomTokenHeader(t *testing.T) {
	source := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{
			Kind:                CredentialKindOAuth,
			Token:               "oauth-token",
			TokenHeader:         "x-api-key",
			ReplaceProviderAuth: true,
		}, nil
	})
	header := doThroughCredentials(t, source, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer sk-configured")
		req.Header.Set("X-Api-Key", "sk-configured")
	})
	if got := header.Get("X-Api-Key"); got != "oauth-token" {
		t.Fatalf("X-Api-Key = %q, want the bare OAuth token", got)
	}
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization survived alongside the token header: %q", got)
	}
}

// 自定义前缀照发；落点是自定义头时不该被默认成 Bearer。
func TestCredentialAuthHeaderDefaults(t *testing.T) {
	cases := []struct {
		name       string
		credential Credential
		wantName   string
		wantValue  string
	}{
		{"默认", Credential{Token: "t"}, "Authorization", "Bearer t"},
		{"自定义头不加前缀", Credential{Token: "t", TokenHeader: "X-Api-Key"}, "X-Api-Key", "t"},
		{"显式前缀", Credential{Token: "t", TokenHeader: "X-Auth", TokenScheme: "Token"}, "X-Auth", "Token t"},
		{"空令牌", Credential{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, value := tc.credential.AuthHeader()
			if name != tc.wantName || value != tc.wantValue {
				t.Fatalf("AuthHeader() = %q, %q; want %q, %q", name, value, tc.wantName, tc.wantValue)
			}
		})
	}
}
