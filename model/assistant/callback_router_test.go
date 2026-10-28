// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallbackRegistryRoutesAndReplaces(t *testing.T) {
	t.Cleanup(func() {
		UnregisterCallbackHandler(PlatformIMessage, "profile-a")
		UnregisterCallbackHandler(PlatformIMessage, "profile-b")
	})

	served := ""
	RegisterCallbackHandler(PlatformIMessage, "profile-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = "a"
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	if !ServeCallback(PlatformIMessage, "profile-a", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("registered handler was not found")
	}
	if served != "a" {
		t.Fatalf("served = %q, want a", served)
	}

	// 没配多机器人时用不带配置档的短地址也要能路由到。
	rec = httptest.NewRecorder()
	if !ServeCallback(PlatformIMessage, "", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("the bare platform address did not route to the only handler")
	}

	// 配置改动后工厂会重建通道，新实例必须顶掉旧的，不然回调还打在已停用的连接上。
	RegisterCallbackHandler(PlatformIMessage, "profile-b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = "b"
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	ServeCallback(PlatformIMessage, "", rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if served != "b" {
		t.Fatalf("served = %q after re-registration, want b", served)
	}

	UnregisterCallbackHandler(PlatformIMessage, "profile-b")
	UnregisterCallbackHandler(PlatformIMessage, "profile-a")
	rec = httptest.NewRecorder()
	if ServeCallback(PlatformIMessage, "profile-a", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("an unregistered platform still served a callback")
	}
}
