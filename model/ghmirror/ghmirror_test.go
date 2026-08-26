// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package ghmirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRewriteOnlyTouchesGitHubDownloads(t *testing.T) {
	const base = "https://ghfast.top"
	download := "https://github.com/SuInk/Diana/releases/download/v1.0.0/diana-webui-linux-amd64.tar.gz"
	if got, want := Rewrite(base, download), base+"/"+download; got != want {
		t.Fatalf("Rewrite() = %q, want %q", got, want)
	}
	// API 不走镜像：公共代理对 API 的支持参差不齐，而版本检查本身只有几 KB。
	api := "https://api.github.com/repos/SuInk/Diana/releases"
	if got := Rewrite(base, api); got != api {
		t.Fatalf("API 地址不该被改写：%q", got)
	}
	// 别人的域名更不能顺手代理走。
	other := "https://example.com/file.tar.gz"
	if got := Rewrite(base, other); got != other {
		t.Fatalf("非 GitHub 地址不该被改写：%q", got)
	}
	// 空前缀等于直连。
	if got := Rewrite("", download); got != download {
		t.Fatalf("空前缀应当原样返回：%q", got)
	}
	// 末尾斜杠不该拼出双斜杠。
	if got := Rewrite(base+"/", download); got != base+"/"+download {
		t.Fatalf("末尾斜杠没有处理：%q", got)
	}
	// http 的 GitHub 地址不算下载地址，别顺手降级。
	if got := Rewrite(base, "http://github.com/a/b"); got != "http://github.com/a/b" {
		t.Fatalf("http 地址不该被改写：%q", got)
	}
}

func TestNormalizeMode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ModeAuto},
		{"auto", ModeAuto},
		{" AUTO ", ModeAuto},
		{"direct", ModeDirect},
		{"https://ghfast.top", "https://ghfast.top"},
		{"https://ghfast.top/", "https://ghfast.top"},
		// 坏地址退回自动，而不是让更新流程带着它跑。
		{"http://ghfast.top", ModeAuto},
		{"https://ghfast.top/?token=x", ModeAuto},
		{"随便写的", ModeAuto},
	} {
		if got := NormalizeMode(tc.in); got != tc.want {
			t.Fatalf("NormalizeMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 自动模式下，直连不通就该挑一条通的镜像。
func TestSelectorFallsBackToMirrorWhenDirectFails(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer mirror.Close()

	selector := newTestSelector([]Mirror{
		{Name: "坏线路", BaseURL: "https://mirror-down.invalid"},
		{Name: "好线路", BaseURL: mirror.URL},
	})
	// 直连指向一个不存在的主机，必然失败。
	base := selector.Base(context.Background(), "https://github.com/SuInk/Diana/releases/download/v1/SHA256SUMS")
	if base != mirror.URL {
		t.Fatalf("没有选中可用镜像：%q", base)
	}
}

// 直连能用时就走直连：链路里少一个第三方。
func TestSelectorPrefersDirectWhenItWorks(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer direct.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer mirror.Close()

	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: mirror.URL}})
	// 用 rewriteHook 把「直连」重定向到本地服务，模拟直连可用。
	selector.client = &http.Client{Transport: rewriteTransport{target: direct.URL, mirror: mirror.URL}}
	if base := selector.Base(context.Background(), "https://github.com/SuInk/Diana/releases/download/v1/SHA256SUMS"); base != "" {
		t.Fatalf("直连可用时不该走镜像：%q", base)
	}
}

// direct 模式永远直连，哪怕镜像更快。
func TestSelectorDirectModeSkipsProbe(t *testing.T) {
	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: "https://mirror.invalid"}})
	selector.SetMode(ModeDirect)
	if base := selector.Base(context.Background(), "https://github.com/a/b/releases/download/v1/x"); base != "" {
		t.Fatalf("direct 模式返回了镜像：%q", base)
	}
	if len(selector.LastProbe()) != 0 {
		t.Fatal("direct 模式不该发起实测")
	}
}

// 手动指定线路时直接用它，不实测。
func TestSelectorManualMode(t *testing.T) {
	selector := newTestSelector(nil)
	selector.SetMode("https://ghfast.top/")
	if base := selector.Base(context.Background(), "https://github.com/a/b/releases/download/v1/x"); base != "https://ghfast.top" {
		t.Fatalf("手动指定的线路没有生效：%q", base)
	}
}

// 换了策略要立刻生效，不能等缓存过期。
func TestSelectorModeChangeInvalidatesCache(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer mirror.Close()
	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: mirror.URL}})
	probeURL := "https://github.com/SuInk/Diana/releases/download/v1/SHA256SUMS"
	if base := selector.Base(context.Background(), probeURL); base != mirror.URL {
		t.Fatalf("首次选择 = %q", base)
	}
	selector.SetMode(ModeDirect)
	if base := selector.Base(context.Background(), probeURL); base != "" {
		t.Fatalf("改成 direct 之后仍然返回镜像：%q", base)
	}
}

// 非下载地址（比如 API）不实测也不加速。
func TestSelectorIgnoresNonDownloadURLs(t *testing.T) {
	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: "https://mirror.invalid"}})
	if base := selector.Base(context.Background(), "https://api.github.com/repos/SuInk/Diana/releases"); base != "" {
		t.Fatalf("API 地址不该走镜像：%q", base)
	}
}

func TestProbeSortsUsableLinesFirst(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer mirror.Close()
	selector := newTestSelector([]Mirror{
		{Name: "坏线路", BaseURL: "https://mirror-down.invalid"},
		{Name: "好线路", BaseURL: mirror.URL},
	})
	results := selector.Probe(context.Background(), "https://github.com/SuInk/Diana/releases/download/v1/SHA256SUMS")
	if len(results) != 3 {
		t.Fatalf("实测结果条数 = %d", len(results))
	}
	if !results[0].OK || results[0].Name != "好线路" {
		t.Fatalf("可用线路没有排在最前：%#v", results)
	}
	last := results[len(results)-1]
	if last.OK || strings.TrimSpace(last.Error) == "" {
		t.Fatalf("失败线路应当带上原因：%#v", last)
	}
}

func newTestSelector(mirrors []Mirror) *Selector {
	selector := NewSelector(&http.Client{Timeout: 3 * time.Second})
	selector.mirrors = mirrors
	selector.probeTimeout = 3 * time.Second
	return selector
}

// rewriteTransport 把直连请求送到本地 direct 服务，镜像请求送到本地 mirror 服务。
type rewriteTransport struct {
	target string
	mirror string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := t.target
	if strings.HasPrefix(req.URL.String(), t.mirror) {
		target = t.mirror
	}
	redirected, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(redirected)
}
