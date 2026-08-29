// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"sort"
	"strings"
)

const (
	repositoryWatchPatchFileLimit   = 4
	repositoryWatchPatchHunkLimit   = 2
	repositoryWatchPatchFileRunes   = 1500
	repositoryWatchPatchDigestRunes = 6000
)

const repositoryWatchPatchTruncated = "（该文件剩余 patch 已省略）"

type repositoryWatchPatchCandidate struct {
	scope string
	file  repositoryWatchDiffFile
}

func renderRepositoryWatchPatchDigest(change repositoryWatchChange) string {
	candidates := make([]repositoryWatchPatchCandidate, 0, repositoryWatchPatchFileLimit*2)
	appendFiles := func(scope string, files []repositoryWatchDiffFile) {
		for _, file := range files {
			if strings.TrimSpace(file.Patch) == "" {
				continue
			}
			candidates = append(candidates, repositoryWatchPatchCandidate{scope: scope, file: file})
		}
	}
	if change.CommitDiff != nil {
		appendFiles("Commit", change.CommitDiff.Files)
	}
	for _, pullRequest := range change.PullRequests {
		appendFiles(fmt.Sprintf("PR #%d", pullRequest.Number), pullRequest.Files)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].file.Changes > candidates[j].file.Changes
	})
	if len(candidates) > repositoryWatchPatchFileLimit {
		candidates = candidates[:repositoryWatchPatchFileLimit]
	}

	sections := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		snippet := compactRepositoryWatchPatch(candidate.file.Patch, repositoryWatchPatchHunkLimit, repositoryWatchPatchFileRunes)
		if snippet == "" {
			continue
		}
		sections = append(sections, fmt.Sprintf(
			"【%s · %s 的受限 patch】\n%s",
			candidate.scope, candidate.file.Filename, snippet,
		))
	}
	return truncateRunes(strings.Join(sections, "\n\n"), repositoryWatchPatchDigestRunes)
}

func compactRepositoryWatchPatch(patch string, hunkLimit, runeLimit int) string {
	if hunkLimit <= 0 || runeLimit <= 0 {
		return ""
	}
	patch = strings.ReplaceAll(strings.ReplaceAll(patch, "\r\n", "\n"), "\r", "\n")
	kept := make([]string, 0, 32)
	hunks := 0
	inHunk := false
	truncated := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunks++
			if hunks > hunkLimit {
				truncated = true
				inHunk = false
				continue
			}
			inHunk = true
			kept = append(kept, line)
		case inHunk && patchChangedLine(line):
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	body := strings.Join(kept, "\n")
	if len([]rune(body)) > runeLimit {
		truncated = true
	}
	if !truncated {
		return body
	}
	markerRunes := len([]rune(repositoryWatchPatchTruncated)) + len([]rune("...")) + 1
	body = truncateRunes(body, max(1, runeLimit-markerRunes))
	return body + "\n" + repositoryWatchPatchTruncated
}

func patchChangedLine(line string) bool {
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return false
	}
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}
