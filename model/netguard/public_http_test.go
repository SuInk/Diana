// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package netguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestValidatePublicURLRejectsPrivateTargets(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "false")
	credentialURL := (&url.URL{
		Scheme: "https",
		User:   url.UserPassword("test-user", "test-password"),
		Host:   "example.com",
	}).String()
	for _, raw := range []string{
		"http://127.0.0.1/admin",
		"http://[::1]/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/",
		"http://localhost/",
		"file:///etc/passwd",
		credentialURL,
	} {
		if err := ValidatePublicURL(context.Background(), raw); err == nil {
			t.Fatalf("ValidatePublicURL(%q) error = nil", raw)
		}
	}
}

func TestValidatePublicURLAcceptsPublicLiteral(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "false")
	if err := ValidatePublicURL(context.Background(), "https://1.1.1.1/"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}

func TestPublicHTTPClientRevalidatesRedirectTargets(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "false")
	client := NewPublicHTTPClient(time.Second)
	redirect, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/private", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(redirect, nil); err == nil {
		t.Fatal("redirect to a private address was accepted")
	}

	publicRedirect, err := http.NewRequest(http.MethodGet, "https://1.1.1.1/public", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(publicRedirect, nil); err != nil {
		t.Fatalf("public redirect rejected: %v", err)
	}
}

func TestStrictValidationIgnoresPrivateFetchOptIn(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "true")
	if err := ValidatePublicURL(context.Background(), "http://127.0.0.1/trusted-media"); err != nil {
		t.Fatalf("explicit trusted private fetch was rejected: %v", err)
	}
	if err := ValidatePublicURLStrict(context.Background(), "http://127.0.0.1/agent-target"); err == nil {
		t.Fatal("strict validation accepted a private agent target")
	}
}

func TestStrictDialCannotUseTrustedMediaPrivateOptIn(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "true")
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "localhost:80", "10.0.0.1:80"} {
		connection, err := DialPublicContextStrict(context.Background(), "tcp", address)
		if connection != nil {
			connection.Close()
		}
		if err == nil {
			t.Fatalf("strict dial connected to %s", address)
		}
	}
}

// 下载内容不设总时限，只有正文停住不动才中断：慢但一直有数据的下载要能跑完，
// 发了一半就卡死的连接要能断开并说清原因。
func TestPublicDownloadClientStopsOnlyWhenBodyStalls(t *testing.T) {
	t.Setenv(allowPrivateFetchesEnv, "true")
	originalStall := downloadStallTimeout
	downloadStallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { downloadStallTimeout = originalStall })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		if r.URL.Path == "/slow" {
			// 总耗时超过停滞时限，但每一段都在时限内。
			for i := 0; i < 5; i++ {
				_, _ = w.Write([]byte("chunk"))
				flusher.Flush()
				time.Sleep(120 * time.Millisecond)
			}
			return
		}
		_, _ = w.Write([]byte("partial"))
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	client := NewPublicDownloadClient()
	if client.Timeout != 0 {
		t.Fatalf("download client still has a total timeout: %v", client.Timeout)
	}
	resp, err := client.Get(server.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "chunkchunkchunkchunkchunk" {
		t.Fatalf("slow but steady download was cut off: body=%q err=%v", body, err)
	}

	resp, err = client.Get(server.URL + "/stall")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, ErrDownloadStalled) {
		t.Fatalf("stalled download error = %v, want ErrDownloadStalled", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stalled download took %v to stop", elapsed)
	}
}
