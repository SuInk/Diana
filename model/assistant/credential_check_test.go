// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestCookieFieldValuePicksFieldFromPastedCookie(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ABC123", "ABC123"},
		{"  ABC123; ", "ABC123"},
		{"MUSIC_U=ABC123", "ABC123"},
		{"music_u=ABC123;", "ABC123"},
		{"_ga=GA1.1.1; NMTID=x=y; MUSIC_U=ABC123; __csrf=c1", "ABC123"},
		{"_ga=GA1.1.1; __csrf=c1", "_ga=GA1.1.1; __csrf=c1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := cookieFieldValue(tc.in, "MUSIC_U"); got != tc.want {
			t.Errorf("cookieFieldValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
