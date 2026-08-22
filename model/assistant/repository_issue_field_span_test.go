// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 写操作要求取值出自用户原文。此前判定按标点切分子句，而逗号是切分符，于是
// 「标题：甲，乙」只能取到「甲」——标题里带逗号是常事，这类标题哪怕用户一字
// 不差地打出来也永远通不过。
func TestRepositoryIssueWriteAcceptsValuesContainingCommas(t *testing.T) {
	repository := "acme/demo"
	cases := []struct {
		name  string
		text  string
		title string
	}{
		{"中文逗号", "给 acme/demo 提 issue，标题：消息段被过滤，影响理解", "消息段被过滤，影响理解"},
		{"英文逗号", "给 acme/demo 提 issue，标题：filtered, breaks meaning", "filtered, breaks meaning"},
		{"多个逗号", "给 acme/demo 提 issue，标题：甲，乙，丙", "甲，乙，丙"},
		{"分号", "给 acme/demo 提 issue，标题：甲；乙", "甲；乙"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, message := validateRepositoryIssueWriteRequest(testCase.text, "create", repository,
				map[string]any{"operation": "create", "repository": repository, "title": testCase.title})
			if code != "" {
				t.Fatalf("用户逐字写出的标题被拒了：%s / %s", code, message)
			}
		})
	}
}

// 放宽取值边界不等于放宽「必须出自用户原文」。
func TestRepositoryIssueWriteStillRejectsValuesTheUserNeverWrote(t *testing.T) {
	repository := "acme/demo"
	cases := []struct {
		name  string
		text  string
		title string
	}{
		{"凭空多出一截", "给 acme/demo 提 issue，标题：消息段被过滤", "消息段被过滤，影响理解"},
		{"完全不同的标题", "给 acme/demo 提 issue，标题：甲", "乙"},
		{"跨到别的字段", "给 acme/demo 提 issue，标题：甲，正文：乙", "甲，正文：乙"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, _ := validateRepositoryIssueWriteRequest(testCase.text, "create", repository,
				map[string]any{"operation": "create", "repository": repository, "title": testCase.title})
			if code != "explicit_fields_required" {
				t.Fatalf("用户没写过的标题应当被拒，实际 code=%q", code)
			}
		})
	}
}

func TestRepositoryIssueCommentAcceptsBodyContainingCommas(t *testing.T) {
	repository := "acme/demo"
	body := "这个问题在 v0.8.52 修好了，可以更新试试"
	code, message := validateRepositoryIssueWriteRequest(
		"在 acme/demo 的 #12 下面回复："+body, "comment", repository,
		map[string]any{"operation": "comment", "repository": repository, "number": 12, "body": body})
	if code != "" {
		t.Fatalf("用户逐字写出的评论被拒了：%s / %s", code, message)
	}
}
