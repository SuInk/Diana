// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"
)

func TestCaptureNetworkJSONRequiresTarget(t *testing.T) {
	if _, err := CaptureNetworkJSON(context.Background(), SandboxedBrowserConfig{}, JSONCaptureRequest{PageURL: "https://example.com"}); err == nil {
		t.Fatal("missing response fragment should fail")
	}
	if _, err := CaptureNetworkJSON(context.Background(), SandboxedBrowserConfig{}, JSONCaptureRequest{URLContains: "/api/"}); err == nil {
		t.Fatal("missing page URL should fail")
	}
	// 私网地址在启动浏览器之前就该被拦住。
	_, err := CaptureNetworkJSON(context.Background(), SandboxedBrowserConfig{}, JSONCaptureRequest{PageURL: "http://127.0.0.1:8080/user", URLContains: "/api/"})
	if err == nil || strings.Contains(err.Error(), "headless") {
		t.Fatalf("private address should be rejected by the URL guard, got %v", err)
	}
}

func TestCaptureCookieParams(t *testing.T) {
	params := captureCookieParams(JSONCaptureRequest{PageURL: "https://www.douyin.com/user/MS4w", Cookie: " ttwid=abc; sessionid=def ;  ; bad "})
	if len(params) != 2 {
		t.Fatalf("params=%#v", params)
	}
	if params[0].Name != "ttwid" || params[0].Value != "abc" || params[0].Domain != "www.douyin.com" || params[0].Path != "/" {
		t.Fatalf("first=%#v", params[0])
	}
	if params[1].Name != "sessionid" || params[1].Value != "def" {
		t.Fatalf("second=%#v", params[1])
	}
	explicit := captureCookieParams(JSONCaptureRequest{PageURL: "https://www.douyin.com/user/MS4w", Cookie: "ttwid=abc", CookieDomain: ".douyin.com"})
	if len(explicit) != 1 || explicit[0].Domain != ".douyin.com" {
		t.Fatalf("explicit=%#v", explicit)
	}
	if params := captureCookieParams(JSONCaptureRequest{PageURL: "https://www.douyin.com/user/MS4w"}); params != nil {
		t.Fatalf("empty cookie should yield no params, got %#v", params)
	}
}
