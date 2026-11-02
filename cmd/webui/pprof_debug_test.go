// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import "testing"

func TestLoopbackListenAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:6060": true,
		"[::1]:6060":     true,
		"0.0.0.0:6060":   false,
		":6060":          false,
		"localhost:6060": false,
		"10.0.0.2:6060":  false,
		"127.0.0.1":      false,
		"127.0.0.1:":     false,
	} {
		if got := loopbackListenAddr(addr); got != want {
			t.Errorf("loopbackListenAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
