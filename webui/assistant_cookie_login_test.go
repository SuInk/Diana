package webui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type youtubeProbeTransport func(*http.Request) (*http.Response, error)

func (f youtubeProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestYouTubeCookieLoginStatesAndScope(t *testing.T) {
	content := "# Netscape HTTP Cookie File\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tselected\n" +
		".google.com\tTRUE\t/\tTRUE\t0\tSID\tother-domain\n" +
		".youtube.com.evil.example\tTRUE\t/\tTRUE\t0\tSID\tlookalike\n" +
		".youtube.com\tTRUE\t/\tTRUE\t1\tSID\texpired\n" +
		"youtube.com\tFALSE\t/\tTRUE\t0\tSID\thost-only\n" +
		".youtube.com\tTRUE\t/feed/lib\tTRUE\t0\tSID\twrong-path\n"
	for _, tc := range []struct {
		name, body, state string
		status            int
		fail              bool
	}{
		{"logged in", `<script>ytcfg.set({"LOGGED_IN":true});</script>`, "valid", 200, false},
		{"logged out", `<script>ytcfg.set({"LOGGED_IN":false});</script>`, "invalid", 200, false},
		{"plain marker", `"LOGGED_IN":true`, "unknown", 200, false},
		{"consent", `<html>Consent required</html>`, "unknown", 200, false},
		{"conflicting", `<script>ytcfg.set({"LOGGED_IN":true});ytcfg.set({"LOGGED_IN":false});</script>`, "unknown", 200, false},
		{"malformed", `<script>ytcfg.set({"LOGGED_IN":"true"});</script>`, "unknown", 200, false},
		{"rate limit", `secret response`, "unknown", 429, false},
		{"redirect", ``, "unknown", 302, false},
		{"network", ``, "unknown", 0, true},
		{"oversize", strings.Repeat("x", 4*1024*1024+1), "unknown", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: youtubeProbeTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != "https://www.youtube.com/feed/library?hl=en" || req.Header.Get("Cookie") != "SID=selected" {
					t.Fatal("unexpected destination or cookie scope")
				}
				if tc.fail {
					return nil, errors.New("network error containing secret")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})}
			result := probeYouTubeCookieLogin(context.Background(), content, client)
			if result.State != tc.state || calls != 1 || strings.Contains(result.Message, "secret") {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
	client := &http.Client{Transport: youtubeProbeTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("request without applicable cookies")
		return nil, nil
	})}
	result := probeYouTubeCookieLogin(context.Background(), "# Netscape HTTP Cookie File\n.google.com\tTRUE\t/\tTRUE\t0\tSID\tx\n", client)
	if result.State != "invalid" {
		t.Fatal(result)
	}
}
