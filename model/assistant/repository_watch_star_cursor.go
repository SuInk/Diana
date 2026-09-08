package assistant

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

func githubEventIDAfter(id, previous string) bool {
	value, err := strconv.ParseUint(id, 10, 64)
	old, oldErr := strconv.ParseUint(previous, 10, 64)
	return err == nil && oldErr == nil && value > old
}

func starEventAfter(item repositoryWatchStargazer, cursorID string, cursorAt time.Time) bool {
	if item.ID == "" || item.ID == repositoryWatchNoStarEvent || item.ID == cursorID {
		return false
	}
	if cursorID == "" || cursorID == repositoryWatchNoStarEvent && cursorAt.IsZero() {
		return true
	}
	if !cursorAt.IsZero() {
		if item.StarredAt.IsZero() || item.StarredAt.Before(cursorAt) {
			return false
		}
		if item.StarredAt.After(cursorAt) {
			return true
		}
	}
	return githubEventIDAfter(item.ID, cursorID)
}

func advanceStarCursor(id string, at time.Time, item repositoryWatchStargazer) (string, time.Time) {
	if item.ID == id {
		if item.StarredAt.After(at) {
			at = item.StarredAt
		}
		return id, at
	}
	if starEventAfter(item, id, at) {
		return item.ID, item.StarredAt
	}
	return id, at
}

func starEventsAfter(events []repositoryWatchStargazer, cursorID string, cursorAt time.Time) []repositoryWatchStargazer {
	cursorID = strings.TrimSpace(cursorID)
	for _, item := range events {
		if item.ID == cursorID && item.StarredAt.After(cursorAt) {
			cursorAt = item.StarredAt
		}
	}
	seen := make(map[string]bool)
	var out []repositoryWatchStargazer
	for _, item := range events {
		if !seen[item.ID] && starEventAfter(item, cursorID, cursorAt) {
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	// Reverse the API order first; stable sorting preserves legacy opaque-ID ties.
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StarredAt.Before(out[j].StarredAt) || out[i].StarredAt.Equal(out[j].StarredAt) && githubEventIDAfter(out[j].ID, out[i].ID)
	})
	return out
}
