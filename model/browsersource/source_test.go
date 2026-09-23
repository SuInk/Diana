// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browsersource

import "testing"

func TestResolvePrefersBoxThenExtension(t *testing.T) {
	cases := []struct {
		box, extension bool
		want           string
	}{
		{false, false, Off},
		{true, false, Box},
		{false, true, Extension},
		{true, true, Box},
	}
	for _, tc := range cases {
		if got := Resolve(tc.box, tc.extension); got != tc.want {
			t.Fatalf("Resolve(%v, %v) = %q, want %q", tc.box, tc.extension, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, value := range []string{Off, Box, Extension} {
		if !Valid(value) {
			t.Fatalf("Valid(%q) = false", value)
		}
	}
	for _, value := range []string{"", "render", "Box"} {
		if Valid(value) {
			t.Fatalf("Valid(%q) = true", value)
		}
	}
}
