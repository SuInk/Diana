package assistant

import (
	"fmt"
	"slices"
	"strings"
)

// The poll result must still belong to the checkpoint it read. Reject a stale
// result before saving its pending notification, not just its cursor fields.
func validateRepositoryWatchProgress(item Reminder, snapshot repositoryWatchSnapshot) error {
	previous := snapshot.previous
	if previous == nil {
		if snapshot.CommitSHA != "" && item.LastCommitSHA != "" && snapshot.CommitSHA != item.LastCommitSHA {
			return fmt.Errorf("仓库订阅 %s 的 Commit 游标缺少祖先校验", item.ID)
		}
		return nil
	}
	stale := !strings.EqualFold(item.Repository, snapshot.repository) || item.RepositoryBranch != snapshot.branch || !item.CancelledAt.IsZero()
	selected := snapshot.selection
	stale = stale || item.WatchCommits != selected.Commits || item.WatchPullRequests != selected.PullRequests || item.WatchIssues != selected.Issues || item.WatchReleases != selected.Releases || item.WatchStars != selected.Stars
	stale = stale || !slices.Equal(item.WatchPullRequestEvents, selected.PullRequestEvents) || !slices.Equal(item.WatchIssueEvents, selected.IssueEvents)
	stale = stale || item.WatchCommits && snapshot.CommitSHA != "" && item.LastCommitSHA != previous.CommitSHA
	stale = stale || item.WatchPullRequests && snapshot.PullRequestCursor != "" && item.LastPullRequestCursor != previous.PullRequestCursor
	stale = stale || item.WatchIssues && snapshot.IssueCursor != "" && item.LastIssueCursor != previous.IssueCursor
	stale = stale || item.WatchReleases && snapshot.ReleaseTag != "" && (item.LastReleaseTag != previous.ReleaseTag || !item.LastReleasePublishedAt.Equal(previous.ReleasePublishedAt) || item.LastReleaseID != previous.ReleaseID)
	stale = stale || item.WatchStars && snapshot.HasStarCount && (item.LastStarEventID != previous.StarEventID || !item.LastStarEventAt.Equal(previous.StarEventAt))
	if stale {
		return fmt.Errorf("仓库订阅 %s 的游标或来源已变化，丢弃旧轮询结果并重新检查", item.ID)
	}
	return nil
}

func applyRepositoryReleaseCursor(item *Reminder, snapshot repositoryWatchSnapshot) {
	if snapshot.ReleaseTag == "" || snapshot.ReleaseTag == repositoryWatchNoReleaseCursor {
		if item.LastReleaseTag == "" {
			item.LastReleaseTag = snapshot.ReleaseTag
		}
		return
	}
	if releaseCursorAfter(snapshot.ReleasePublishedAt, snapshot.ReleaseID, item.LastReleasePublishedAt, item.LastReleaseID) {
		item.LastReleaseTag = snapshot.ReleaseTag
		item.LastReleasePublishedAt = snapshot.ReleasePublishedAt
		item.LastReleaseID = snapshot.ReleaseID
	}
}
