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

// oauthImageSource 是配了 OAuth 的提供商客户端：令牌在传输层注入，每个请求都带。
func oauthImageSource(baseURL, token string) imageEditSource {
	credentials := CredentialSourceFunc(func(context.Context) (Credential, error) {
		return Credential{Kind: CredentialKindOAuth, Token: token}, nil
	})
	return newImageEditSource(ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: baseURL + "/v1"}, httpClientWithCredentials(http.DefaultClient, credentials))
}

// 提供商自己的文件（上一轮生成的图）照样带令牌取。
func TestImageEditDownloadSendsTokenToProviderHost(t *testing.T) {
	token := "oauth-" + "provider0123456789"
	provider := newImageServer(t, nil)
	source := oauthImageSource(provider.URL, token)
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

// 用户或模型给的别处链接：不带令牌。这里放开私有地址，只看凭据。
func TestImageEditDownloadDoesNotSendTokenToOtherHost(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	token := "oauth-" + "other0123456789ab"
	provider := newImageServer(t, nil)
	other := newImageServer(t, nil)
	source := oauthImageSource(provider.URL, token)
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

// 提供商那边跳到别处的重定向不跟：令牌是在每一跳上重新注入的。
func TestImageEditDownloadDoesNotFollowProviderRedirectOffHost(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	token := "oauth-" + "redirect0123456789"
	other := newImageServer(t, nil)
	provider := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen.png", http.StatusFound)
	})
	source := oauthImageSource(provider.URL, token)
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
	source := oauthImageSource(provider.URL, "oauth-"+"private0123456789")
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
