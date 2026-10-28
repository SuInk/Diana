// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package hostinfo

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyIP(t *testing.T) {
	cases := map[string]AddressScope{
		"127.0.0.1":   ScopeLoopback,
		"::1":         ScopeLoopback,
		"192.168.1.5": ScopeLAN,
		"10.2.3.4":    ScopeLAN,
		"172.16.0.9":  ScopeLAN,
		"100.65.0.1":  ScopeLAN,
		"fd00::1":     ScopeLAN,
		"169.254.1.1": ScopeLinkLocal,
		"fe80::1":     ScopeLinkLocal,
		"203.0.113.7": ScopePublic,
		"2001:db8::1": ScopePublic,
	}
	for raw, want := range cases {
		if got := ClassifyIP(net.ParseIP(raw)); got != want {
			t.Errorf("%s = %s, want %s", raw, got, want)
		}
	}
}

func TestListensOn(t *testing.T) {
	lan := net.ParseIP("192.168.1.5")
	loop := net.ParseIP("127.0.0.1")
	if !listensOn("", lan) || !listensOn("0.0.0.0", lan) || !listensOn("::", lan) {
		t.Fatal("wildcard should cover lan")
	}
	if listensOn("127.0.0.1", lan) || !listensOn("127.0.0.1", loop) || !listensOn("localhost", loop) {
		t.Fatal("specific host mismatch")
	}
}

func TestLocalAddressesHasLoopbackURL(t *testing.T) {
	addrs, err := LocalAddresses("", "18080")
	if err != nil {
		t.Skip(err)
	}
	for _, addr := range addrs {
		if addr.Scope == ScopeLoopback && addr.Family == "ipv4" {
			if addr.URL != "http://127.0.0.1:18080" {
				t.Fatalf("url = %q", addr.URL)
			}
			return
		}
	}
	t.Skip("no ipv4 loopback")
}

func TestDetectPublicIPFallsBackAndCaches(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("nope")) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("203.0.113.7\n")) }))
	defer good.Close()
	old := publicIPServices
	publicIPServices = []string{bad.URL, good.URL}
	defer func() { publicIPServices = old }()
	got := DetectPublicIP(context.Background(), bad.Client(), true)
	if got.IP != "203.0.113.7" || got.Source != good.URL {
		t.Fatalf("got %+v", got)
	}
	good.Close()
	if cached := DetectPublicIP(context.Background(), bad.Client(), false); cached.IP != "203.0.113.7" {
		t.Fatalf("cache miss %+v", cached)
	}
}
