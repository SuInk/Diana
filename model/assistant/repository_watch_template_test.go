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

// 只开放整体模板：自定义能改标题行、概括位置和是否分条，条目排版固定不动。
func TestRepositoryWatchCustomTemplateChangesTheLayout(t *testing.T) {
	templates := repositoryWatchTemplatesFromSettings(SettingValues{
		repositoryWatchSettingTemplateHeader: "【{repository}】\n{body}\n{summary}",
	})
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
	// 条目排版不受设置影响，始终是统一的两行。
	if !strings.HasPrefix(body, "Release v0.9.0\n发布于 ") || !strings.HasSuffix(body, "https://github.com/SuInk/Diana/releases/tag/v0.9.0") {
		t.Fatalf("entry layout should stay fixed: %q", body)
	}
	message := composeRepositoryWatchMessageWithTemplate(templates.Header, "SuInk/Diana", body)
	if !strings.HasPrefix(message, "【SuInk/Diana】") || !strings.Contains(message, "Release v0.9.0") {
		t.Fatalf("custom header template ignored: %q", message)
	}
	if chunks := splitNotification(message, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("template without a split marker should stay in one message: %#v", chunks)
	}
}

// 条目排版不再是设置项：只留一个模板，五类动态的形状由代码保证一致。
func TestRepositoryWatchEntryTemplatesAreNotConfigurable(t *testing.T) {
	templates := repositoryWatchTemplatesFromSettings(SettingValues{
		repositoryWatchSettingTemplateHeader: "【{repository}】\n{body}",
		"template_release":                   "🎉 {tag} {name}（{time}）{url}",
		"template_commit":                    "{sha}",
	})
	if templates.Release != repositoryWatchDefaultReleaseTemplate || templates.Commit != repositoryWatchDefaultCommitTemplate {
		t.Fatalf("entry templates must ignore settings: %+v", templates)
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

// 分条符仍然可用：自定义模板里写一行 <botbr> 就从那里分成下一条消息。
func TestRepositoryWatchTemplateKeepsExplicitSplitMarker(t *testing.T) {
	message := composeRepositoryWatchMessageWithTemplate(
		"【{repository}】\n<botbr>\n{body}", "SuInk/Diana", "明细")
	if message != "【SuInk/Diana】\n<botbr>\n明细" {
		t.Fatalf("message = %q", message)
	}
	chunks := splitNotification(message, notificationChunkSize)
	if len(chunks) != 2 || chunks[1] != "明细" {
		t.Fatalf("chunks = %#v", chunks)
	}

	// 正文为空时不能留下孤立的分条符，否则会多发一条空消息。
	empty := composeRepositoryWatchMessageWithTemplate(
		"【{repository}】\n<botbr>\n{body}", "SuInk/Diana", "")
	if strings.Contains(empty, notificationSplitMarker) {
		t.Fatalf("dangling split marker: %q", empty)
	}
}
