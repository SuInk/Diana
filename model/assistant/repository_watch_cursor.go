package assistant

import (
	"fmt"
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

func logRepositoryOpaqueCursorRetained(repository, kind, current, candidate, reason string) {
	log.Printf("diana repository_watch cursor retained: repository=%q kind=%s previous=%q observed=%q reason=%q", repository, kind, current, candidate, reason)
}

// repositoryWatchCursorFields 是一条仓库订阅里会随轮询前移的全部游标。
type repositoryWatchCursorFields struct {
	commit, pullRequest, issue, release, star string
	releaseAt, starAt                         time.Time
	releaseID                                 int64
}

func repositoryWatchCursorFieldsOf(item Reminder) repositoryWatchCursorFields {
	return repositoryWatchCursorFields{
		commit:      item.LastCommitSHA,
		pullRequest: item.LastPullRequestCursor,
		issue:       item.LastIssueCursor,
		release:     item.LastReleaseTag,
		releaseAt:   item.LastReleasePublishedAt,
		releaseID:   item.LastReleaseID,
		star:        item.LastStarEventID,
		starAt:      item.LastStarEventAt,
	}
}

// repositoryWatchCursorChanges 列出前后不同的游标，格式是 name=前->后，全都没变时
// 返回空串。时间按 Equal 比较，只差时区或单调时钟读数的不算变化。
func repositoryWatchCursorChanges(before, after repositoryWatchCursorFields) string {
	var changes []string
	text := func(name, previous, next string) {
		if previous != next {
			changes = append(changes, fmt.Sprintf("%s=%q->%q", name, previous, next))
		}
	}
	instant := func(name string, previous, next time.Time) {
		if !previous.Equal(next) {
			changes = append(changes, fmt.Sprintf("%s=%s->%s", name, previous.UTC().Format(time.RFC3339Nano), next.UTC().Format(time.RFC3339Nano)))
		}
	}
	text("commit", before.commit, after.commit)
	text("pr", before.pullRequest, after.pullRequest)
	text("issue", before.issue, after.issue)
	text("release", before.release, after.release)
	instant("release_at", before.releaseAt, after.releaseAt)
	if before.releaseID != after.releaseID {
		changes = append(changes, fmt.Sprintf("release_id=%d->%d", before.releaseID, after.releaseID))
	}
	text("star", before.star, after.star)
	instant("star_at", before.starAt, after.starAt)
	return strings.Join(changes, " ")
}
