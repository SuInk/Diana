// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n0123456789")

// imageServer 记下每次请求带的 Authorization，回一张 PNG。
type imageServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newImageServer(t *testing.T, handler http.HandlerFunc) *imageServer {
	t.Helper()
	server := &imageServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.auths = append(server.auths, r.Header.Get("Authorization"))
		server.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *imageServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

func apiKeyImageSource(baseURL, key string) imageEditSource {
	return newImageEditSource(ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: baseURL + "/v1", APIKey: key}, http.DefaultClient)
}

// 提供商自己的文件（上一轮生成的图）照样带令牌取。
func TestImageEditDownloadSendsAPIKeyToProviderHost(t *testing.T) {
	token := "sk-" + "provider0123456789"
	provider := newImageServer(t, nil)
	source := apiKeyImageSource(provider.URL, token)
	got, err := imageEditInputFrom(context.Background(), source, provider.URL+"/files/out.png", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.data) != string(testPNG) {
		t.Fatalf("源图内容不对：%q", got.data)
	}
	if auths := provider.received(); len(auths) != 1 || auths[0] != "Bearer "+token {
		t.Fatalf("提供商自己的主机应当收到令牌：%v", auths)
	}
}

func TestImageEditDownloadGeminiUsesAPIKeyAndCustomHeadersOnlyOnProvider(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	key := "gemini-key-0123456789"
	provider := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != key || r.Header.Get("X-Gateway-Key") != "custom-secret" {
			t.Errorf("provider request headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	})
	other := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "X-Goog-Api-Key", "X-Gateway-Key"} {
			if r.Header.Get(name) != "" {
				t.Errorf("external host received %s", name)
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	})
	source := newImageEditSource(ProviderConfig{
		Provider: ProviderGemini,
		BaseURL:  provider.URL,
		APIKey:   key,
		Headers:  map[string]string{"X-Gateway-Key": "custom-secret"},
	}, nil)
	for _, endpoint := range []string{provider.URL, other.URL} {
		if _, err := imageEditInputFrom(context.Background(), source, endpoint+"/image.png", 0); err != nil {
			t.Fatal(err)
		}
	}
}

// 用户或模型给的别处链接：不带令牌。这里放开私有地址，只看凭据。
func TestImageEditDownloadDoesNotSendAPIKeyToOtherHost(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	token := "sk-" + "other0123456789ab"
	provider := newImageServer(t, nil)
	other := newImageServer(t, nil)
	source := apiKeyImageSource(provider.URL, token)
	if _, err := imageEditInputFrom(context.Background(), source, other.URL+"/cat.png", 0); err != nil {
		t.Fatal(err)
	}
	if auths := other.received(); len(auths) != 1 || auths[0] != "" {
		t.Fatalf("别的主机不该收到 Authorization：%v", auths)
	}
	if len(provider.received()) != 0 {
		t.Fatal("下载别处的图不该碰提供商")
	}
}

// 提供商那边跳到别处的重定向不跟，避免转发 API Key 和自定义请求头。
func TestImageEditDownloadDoesNotFollowProviderRedirectOffHost(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	token := "sk-" + "redirect0123456789"
	other := newImageServer(t, nil)
	provider := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen.png", http.StatusFound)
	})
	source := apiKeyImageSource(provider.URL, token)
	if _, err := imageEditInputFrom(context.Background(), source, provider.URL+"/files/out.png", 0); err == nil {
		t.Fatal("跳出提供商主机的重定向应当报错")
	}
	for _, auth := range other.received() {
		if strings.Contains(auth, token) {
			t.Fatalf("重定向目标收到了令牌：%v", other.received())
		}
	}
}

// 别处的链接走 SSRF 防线：内网、回环、元数据服务一律拒绝，请求根本发不出去。
func TestImageEditDownloadRejectsPrivateAddresses(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "")
	provider := newImageServer(t, nil)
	internal := newImageServer(t, nil)
	source := apiKeyImageSource(provider.URL, "sk-"+"private0123456789")
	for _, target := range []string{
		internal.URL + "/secret.png",
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://10.0.0.1/a.png",
		"http://localhost/a.png",
	} {
		if _, err := imageEditInputFrom(context.Background(), source, target, 0); err == nil {
			t.Fatalf("%s 应当被拒绝", target)
		}
	}
	if len(internal.received()) != 0 {
		t.Fatalf("内网主机不该收到任何请求：%v", internal.received())
	}
}
