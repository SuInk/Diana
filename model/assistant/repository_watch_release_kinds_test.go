// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 只订正式版的人不该被 rc 版打扰，但游标必须照样越过 rc：否则一旦重新勾上预发布，
// 攒下来的 rc 会一次性全补推出来。
func TestRepositoryReleaseKindsFilterNotificationsWithoutHoldingCursor(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	release := func(tag string, id int, day int, prerelease bool) map[string]any {
		return map[string]any{
			"tag_name": tag, "id": id, "name": tag, "published_at": at.AddDate(0, 0, day),
			"draft": false, "prerelease": prerelease,
		}
	}
	stable, candidate, next := release("v1.0.0", 1, 0, false), release("v1.1.0-rc.1", 2, 1, true), release("v1.1.0", 3, 2, false)
	f.set("/repos/acme/demo/releases", []any{next, candidate, stable})
	cursor := repositoryWatchSnapshot{ReleaseTag: "v1.0.0", ReleasePublishedAt: at, ReleaseID: 1}

	for _, tc := range []struct {
		name  string
		kinds []string
		want  []string
	}{
		// 列表按发布时间倒序，最新的排最前。
		{"旧订阅未配置按全选", nil, []string{"v1.1.0", "v1.1.0-rc.1"}},
		{"只要正式版", []string{repositoryWatchReleaseKindStable}, []string{"v1.1.0"}},
		{"只要预发布", []string{repositoryWatchReleaseKindPrerelease}, []string{"v1.1.0-rc.1"}},
		{"两种都不要", []string{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, advanced, err := p.fetchReleases(context.Background(), "acme/demo", cursor, repositoryWatchSelection{ReleaseKinds: tc.kinds}, nil)
			if err != nil {
				t.Fatalf("fetch releases: %v", err)
			}
			tags := make([]string, 0, len(got))
			for _, item := range got {
				tags = append(tags, item.Tag)
			}
			if strings.Join(tags, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("notified=%v want=%v", tags, tc.want)
			}
			// 种类过滤完全不碰游标：四种配置都停在最新的那一条上。
			if advanced.ReleaseTag != "v1.1.0" || advanced.ReleaseID != 3 {
				t.Fatalf("cursor=%#v", advanced)
			}
		})
	}
}

// 关掉预发布期间发布的 rc，在重新勾上之后也不补推。
func TestRepositoryReleaseKindsReenabledDoesNotReplaySkippedPrereleases(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.set("/repos/acme/demo/releases", []any{
		map[string]any{"tag_name": "v2.0.0-rc.1", "id": 2, "name": "rc", "published_at": at.AddDate(0, 0, 1), "prerelease": true},
		map[string]any{"tag_name": "v1.0.0", "id": 1, "name": "stable", "published_at": at},
	})
	cursor := repositoryWatchSnapshot{ReleaseTag: "v1.0.0", ReleasePublishedAt: at, ReleaseID: 1}
	got, advanced, err := p.fetchReleases(context.Background(), "acme/demo", cursor, repositoryWatchSelection{ReleaseKinds: []string{repositoryWatchReleaseKindStable}}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("stable only notified=%v err=%v", got, err)
	}
	got, _, err = p.fetchReleases(context.Background(), "acme/demo", advanced, repositoryWatchSelection{}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("replayed after re-enabling=%v err=%v", got, err)
	}
}

// 被过滤掉的预发布之后被删除——rc 转正后删掉预发布是常见操作。游标记的是发布时间
// 加 ID 的水位，不指望那个 tag 还在，所以删掉既不会报错，也不会把更旧的正式版当成
// 新动态重推一遍。
func TestRepositoryReleaseKindsSurviveDeletedSkippedPrerelease(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	release := func(tag string, id int, day int, prerelease bool) map[string]any {
		return map[string]any{
			"tag_name": tag, "id": id, "name": tag, "published_at": at.AddDate(0, 0, day),
			"draft": false, "prerelease": prerelease,
		}
	}
	path := "/repos/acme/demo/releases"
	stable, candidate, next := release("v1.0.0", 1, 0, false), release("v1.1.0-rc.1", 2, 1, true), release("v1.1.0", 3, 2, false)
	stableOnly := repositoryWatchSelection{ReleaseKinds: []string{repositoryWatchReleaseKindStable}}

	// rc 不推，但游标照样越过它——它现在停在一条从没通知过的记录上。
	f.set(path, []any{candidate, stable})
	got, cursor, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{ReleaseTag: "v1.0.0", ReleasePublishedAt: at, ReleaseID: 1}, stableOnly, nil)
	if err != nil || len(got) != 0 || cursor.ReleaseTag != "v1.1.0-rc.1" || cursor.ReleaseID != 2 {
		t.Fatalf("skipped prerelease=%v cursor=%#v err=%v", got, cursor, err)
	}

	// rc 被删除，列表里只剩更旧的正式版：保留水位，不倒退也不重推。
	f.set(path, []any{stable})
	got, retained, err := p.fetchReleases(context.Background(), "acme/demo", cursor, stableOnly, nil)
	if err != nil || len(got) != 0 || retained.ReleaseTag != cursor.ReleaseTag || retained.ReleaseID != cursor.ReleaseID {
		t.Fatalf("after delete=%v cursor=%#v err=%v", got, retained, err)
	}

	// 正式版发布后照常推一次，且只推这一条。
	f.set(path, []any{next, stable})
	got, advanced, err := p.fetchReleases(context.Background(), "acme/demo", retained, stableOnly, nil)
	if err != nil || len(got) != 1 || got[0].Tag != "v1.1.0" || advanced.ReleaseID != 3 {
		t.Fatalf("after next release=%v cursor=%#v err=%v", got, advanced, err)
	}
}

// 两类混在同一条通知里时，预发布要看得出来，正式版保持原样。
func TestRepositoryWatchRendersPrereleaseMarker(t *testing.T) {
	at := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	stamp := formatRepositoryWatchTime(at)
	result := renderRepositoryWatchChanges(repositoryWatchChange{
		Releases: []repositoryWatchRelease{
			{Tag: "v1.1.0-rc.1", PublishedAt: at, URL: "https://github.com/acme/demo/releases/tag/v1.1.0-rc.1", Prerelease: true},
			{Tag: "v1.0.0", PublishedAt: at, URL: "https://github.com/acme/demo/releases/tag/v1.0.0"},
		},
	})
	for _, want := range []string{
		"Release v1.1.0-rc.1（预发布）\n发布于 " + stamp,
		"Release v1.0.0\n发布于 " + stamp,
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("rendered releases missing %q:\n%s", want, result)
		}
	}
}

// 聊天工具改 Release 版本种类：落库、回显和校验都要跟着走。
func TestRepositoryWatchChatToolUpdatesReleaseKinds(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "manager"}
	tool, store := repositoryWatchChatToolFixture(t, event, false)
	for i := range store.items {
		if store.items[i].ID == "watch-demo" {
			// 类型开关不动，才不会触发重新建基线的网络请求。
			store.items[i].WatchReleases = true
		}
	}

	updated := runRepositoryWatchChatTool(t, tool, map[string]any{
		"operation": "update", "id": "watch-demo", "release_kinds": []any{"stable"},
	})
	if !updated.OK || updated.Watch == nil || strings.Join(updated.Watch.ReleaseKinds, ",") != "stable" {
		t.Fatalf("update = %#v", updated)
	}
	for _, item := range store.items {
		if item.ID == "watch-demo" && strings.Join(item.WatchReleaseKinds, ",") != "stable" {
			t.Fatalf("落库的 Release 种类没改对: %#v", item.WatchReleaseKinds)
		}
	}

	// 空数组是「一种都不收」，不能被当成没配置而回落成全选。
	cleared := runRepositoryWatchChatTool(t, tool, map[string]any{
		"operation": "update", "id": "watch-demo", "release_kinds": []any{},
	})
	if !cleared.OK || cleared.Watch == nil || len(cleared.Watch.ReleaseKinds) != 0 {
		t.Fatalf("cleared = %#v", cleared)
	}
	for _, item := range store.items {
		if item.ID == "watch-demo" && (item.WatchReleaseKinds == nil || len(item.WatchReleaseKinds) != 0) {
			t.Fatalf("清空后应落成显式空数组: %#v", item.WatchReleaseKinds)
		}
	}

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "update", "id": "watch-demo", "release_kinds": []any{"draft"},
	}); err == nil {
		t.Fatal("草稿不是可选的版本种类，应该报错")
	}
}
