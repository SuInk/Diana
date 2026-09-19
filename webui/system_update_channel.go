package webui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Only documented release tags can enter an update channel. Unknown prereleases
// (including source builds) never become downloadable update candidates.
var channelTagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-(beta|rc)\.(0|[1-9][0-9]*))?(?:\+[0-9A-Za-z.-]+)?$`)

func releaseAllowed(release ReleaseEntry, channel string) bool {
	match := channelTagPattern.FindStringSubmatch(release.Tag)
	if match == nil {
		return false
	}
	switch match[1] {
	case "":
		return !release.Prerelease
	case "rc":
		return channel == "beta"
	case "beta":
		return channel == "beta"
	}
	return false
}

func (h *SystemUpdateHandler) latestChannelRelease(ctx context.Context, remoteURL string) (ReleaseEntry, error) {
	owner, repo, ok := githubRepoFromRemote(remoteURL)
	if !ok {
		owner, repo = defaultReleaseOwner, defaultReleaseRepo
	}
	releases, err := h.githubReleases(ctx, owner, repo, 30)
	if err != nil {
		return ReleaseEntry{}, err
	}
	channel := h.currentPolicy().Channel
	var latest ReleaseEntry
	for _, release := range releases {
		if !releaseAllowed(release, channel) {
			continue
		}
		newer, _ := isNewerVersion(latest.Tag, release.Tag)
		if latest.Tag == "" || newer {
			latest = release
		}
	}
	if latest.Tag == "" {
		return ReleaseEntry{}, fmt.Errorf("更新通道 %s 暂无可用版本", channel)
	}
	return latest, nil
}

// newerPrerelease compares equal version cores using SemVer prerelease ordering.
// Source -dev builds retain the existing same-baseline behavior; source builds
// are separately excluded from automatic updates.
func newerPrerelease(current, latest string) bool {
	suffix := func(value string) string {
		value = strings.SplitN(value, "+", 2)[0]
		if _, pre, ok := strings.Cut(value, "-"); ok {
			return pre
		}
		return ""
	}
	a, b := suffix(current), suffix(latest)
	if a == "dev" {
		a = ""
	}
	if a == b || a == "" {
		return false
	}
	if b == "" {
		return true
	}
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	numeric := func(s string) bool {
		if s == "" {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(left) && i < len(right); i++ {
		x, y := left[i], right[i]
		if x == y {
			continue
		}
		nx, ny := numeric(x), numeric(y)
		if nx != ny {
			return nx
		}
		if nx && len(x) != len(y) {
			return len(y) > len(x)
		}
		return y > x
	}
	return len(right) > len(left)
}
