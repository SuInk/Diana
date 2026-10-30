// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package qqcall

import "testing"

func TestNameMatches(t *testing.T) {
	if !NameMatches("win ter", "", "Winter") {
		t.Fatal("case and spaces should not matter")
	}
	if NameMatches("Winter2", "Winter") {
		t.Fatal("a longer name must not match")
	}
	if NameMatches("", "") {
		t.Fatal("empty names never match")
	}
}
