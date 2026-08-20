// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestRenderRepositoryWatchTemplateDropsLinesWithOnlyEmptyPlaceholders(t *testing.T) {
	template := "PR #{number}（{status}）\n{title}\n作者：{author}\n{url}\n静态提示行"
	rendered := renderRepositoryWatchTemplate(template, map[string]string{
		"number": "7",
		"status": "已合并",
		"title":  "修复触发别名",
		"author": "",
		"url":    "https://example.com/pr/7",
	})
	want := "PR #7（已合并）\n修复触发别名\nhttps://example.com/pr/7\n静态提示行"
	if rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
}

func TestRepositoryWatchCustomTemplatesChangeTheLayout(t *testing.T) {
	settings := SettingValues{
		repositoryWatchSettingTemplateHeader:  "【{repository}】\n{body}\n{summary}",
		repositoryWatchSettingTemplateRelease: "🎉 {tag} {name}（{time}）{url}",
	}
	templates := repositoryWatchTemplatesFromSettings(settings)
	change := repositoryWatchChange{
		Repository: "SuInk/Diana",
		Releases: []repositoryWatchRelease{{
			Tag:         "v0.9.0",
			Name:        "Diana v0.9.0",
			URL:         "https://github.com/SuInk/Diana/releases/tag/v0.9.0",
			PublishedAt: time.Date(2026, 8, 19, 10, 0, 0, 0, time.Local),
		}},
	}

	body := renderRepositoryWatchChangesWithTemplates(change, templates)
	if !strings.HasPrefix(body, "🎉 v0.9.0") || !strings.Contains(body, "https://github.com/SuInk/Diana/releases/tag/v0.9.0") {
		t.Fatalf("custom release template ignored: %q", body)
	}
	message := composeRepositoryWatchMessageWithTemplate(templates.Header, "SuInk/Diana", body, "发布了新版本。")
	if !strings.HasPrefix(message, "【SuInk/Diana】") || !strings.HasSuffix(message, "发布了新版本。") {
		t.Fatalf("custom header template ignored: %q", message)
	}

	// 未覆盖的类别继续用默认模板。
	if templates.Commit != repositoryWatchDefaultCommitTemplate {
		t.Fatalf("commit template should fall back to default, got %q", templates.Commit)
	}
}

func TestRepositoryWatchTemplatesFallBackToDefaultsWhenBlank(t *testing.T) {
	templates := repositoryWatchTemplatesFromSettings(SettingValues{
		repositoryWatchSettingTemplateHeader: "   ",
	})
	if templates != defaultRepositoryWatchTemplates() {
		t.Fatalf("blank settings must resolve to defaults: %+v", templates)
	}
}

// 分条符是模板里的普通静态行：概括有内容时保留，没内容时随空段一起清掉。
func TestRepositoryWatchTemplateKeepsExplicitSplitMarker(t *testing.T) {
	message := composeRepositoryWatchMessageWithTemplate(
		"【{repository}】\n{body}\n<botbr>\n{summary}", "SuInk/Diana", "明细", "概括")
	if message != "【SuInk/Diana】\n明细\n<botbr>\n概括" {
		t.Fatalf("message = %q", message)
	}
	chunks := splitNotification(message, notificationChunkSize)
	if len(chunks) != 2 || chunks[1] != "概括" {
		t.Fatalf("chunks = %#v", chunks)
	}

	// 想让概括跟在正文后面而不分条时，去掉那一行就行。
	inline := composeRepositoryWatchMessageWithTemplate(
		"【{repository}】\n{body}\n{summary}", "SuInk/Diana", "明细", "概括")
	if chunks := splitNotification(inline, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("inline template should stay in one message, got %#v", chunks)
	}
}
