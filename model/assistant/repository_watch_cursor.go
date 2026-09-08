package assistant

import (
	"log"
	"strconv"
	"strings"
	"time"
)

// Issue and PR cursors share the ordered (updated_at, number) format.
// Empty and __none__ are initialization states, never a reset of a watermark.
func advanceRepositoryWatchCursor(current, candidate string) string {
	current, candidate = strings.TrimSpace(current), strings.TrimSpace(candidate)
	if candidate == "" {
		return current
	}
	if candidate == repositoryWatchNoIssueCursor {
		if current == "" {
			return candidate
		}
		return current
	}
	at, number, valid := parseRepositoryWatchCursor(candidate)
	if !valid {
		return current
	}
	previousAt, previousNumber, previousValid := parseRepositoryWatchCursor(current)
	if !previousValid || at.After(previousAt) || at.Equal(previousAt) && number > previousNumber {
		return candidate
	}
	return current
}

func parseRepositoryWatchCursor(cursor string) (time.Time, int, bool) {
	cursor = strings.TrimSpace(cursor)
	separator := strings.LastIndex(cursor, "#")
	if separator <= 0 || separator == len(cursor)-1 {
		return time.Time{}, 0, false
	}
	number, err := strconv.Atoi(cursor[separator+1:])
	if err != nil || number <= 0 {
		return time.Time{}, 0, false
	}
	// Older timestamp-less cursors can still advance by number or real time.
	if cursor[:separator] == "0" {
		return time.Time{}, number, true
	}
	at, err := time.Parse(time.RFC3339Nano, cursor[:separator])
	return at, number, err == nil
}

func observedRepositoryWatchCursor(repository, kind, current, candidate string) string {
	next := advanceRepositoryWatchCursor(current, candidate)
	if candidate != "" && next != candidate {
		log.Printf("diana repository_watch cursor retained: repository=%q kind=%s previous=%q observed=%q retained=%q", repository, kind, current, candidate, next)
	}
	return next
}
