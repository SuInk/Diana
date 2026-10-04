package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTestTransport func(*http.Request) (*http.Response, error)

func (f releaseTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubReleaseURLMapping(t *testing.T) {
	cases := map[string]string{
		"https://github.com/go-gitea/gitea/releases":             "https://api.github.com/repos/go-gitea/gitea/releases/latest",
		"https://github.com/go-gitea/gitea/releases/latest":      "https://api.github.com/repos/go-gitea/gitea/releases/latest",
		"https://github.com/go-gitea/gitea/releases/tag/v1.27.3": "https://api.github.com/repos/go-gitea/gitea/releases/tags/v1.27.3",
		"https://github.com/acme/tool/releases/tag/pkg/v2":       "https://api.github.com/repos/acme/tool/releases/tags/pkg%2Fv2",
		"https://github.com/acme/tool/releases?page=2":           "",
		"https://github.com.evil.example/acme/tool/releases":     "",
		"https://token@github.com/acme/tool/releases":            "",
		"https://github.com/acme/tool/issues":                    "",
		"https://github.com/acme/../releases":                    "",
	}
	for raw, want := range cases {
		got, ok := githubReleaseAPIURL(raw)
		if got != want || ok != (want != "") {
			t.Fatalf("%s: %q %v", raw, got, ok)
		}
	}
}

func releaseFixture() githubReleaseRecord {
	return githubReleaseRecord{Tag: "v1.27.3", Name: "Gitea 1.27.3", HTMLURL: "https://github.com/go-gitea/gitea/releases/tag/v1.27.3", PublishedAt: "2026-08-29T17:42:17Z", Body: "security and bug fixes"}
}

func releaseFixtureClient(status int, release githubReleaseRecord) *http.Client {
	return &http.Client{Transport: releaseTestTransport(func(req *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(release)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
}

func TestGitHubReleaseFailuresAreNotNegativeEvidence(t *testing.T) {
	for _, status := range []int{403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			page, err := fetchGitHubRelease(context.Background(), releaseFixtureClient(status, releaseFixture()), "https://github.com/go-gitea/gitea/releases", "https://api.github.com/repos/go-gitea/gitea/releases/latest", 8000)
			if err == nil || page.Text != "" || !strings.Contains(err.Error(), "does not establish") {
				t.Fatalf("HTTP %d treated as evidence: %+v %v", status, page, err)
			}
		})
	}
}

func TestGitHubReleaseRejectsMismatchedAndIncompleteRecords(t *testing.T) {
	for _, change := range []func(*githubReleaseRecord){func(r *githubReleaseRecord) { r.HTMLURL = "https://evil.example/release" }, func(r *githubReleaseRecord) { r.PublishedAt = "" }, func(r *githubReleaseRecord) { r.Prerelease = true }, func(r *githubReleaseRecord) { r.Draft = true }, func(r *githubReleaseRecord) { r.HTMLURL = "https://github.com/another/repo/releases/tag/v1.27.3" }} {
		release := releaseFixture()
		change(&release)
		if _, err := fetchGitHubRelease(context.Background(), releaseFixtureClient(200, release), "https://github.com/go-gitea/gitea/releases", "https://api.github.com/repos/go-gitea/gitea/releases/latest", 8000); err == nil {
			t.Fatalf("invalid record accepted: %+v", release)
		}
	}
	release := releaseFixture()
	release.Prerelease = true
	page, err := fetchGitHubRelease(context.Background(), releaseFixtureClient(200, release), release.HTMLURL, "https://api.github.com/repos/go-gitea/gitea/releases/tags/v1.27.3", 1000)
	if err != nil || !strings.Contains(page.Text, "预发布：true") {
		t.Fatalf("explicit prerelease lost: %v %s", err, page.Text)
	}
	if _, err := fetchGitHubRelease(context.Background(), releaseFixtureClient(200, release), release.HTMLURL, "https://api.github.com/repos/go-gitea/gitea/releases/tags/v1.27.2", 1000); err == nil {
		t.Fatal("different tag was accepted")
	}
}
