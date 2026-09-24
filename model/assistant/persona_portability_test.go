// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"slices"
	"testing"
	"time"
)

// TestImportReportsUnknownStyles 认不出来的风格要点名。
//
// Normalized() 把它们静默退回「助手」。对导出文件无所谓（值是本机写出去的），
// 但人设文件是文档里明说可以手写的，也会从别的版本导过来——静默降级的话，用户
// 看到「导入成功」，跑起来完全不是那个语气，而且没有任何线索指向拼错的词。
func TestImportReportsUnknownStyles(t *testing.T) {
	now := time.Now()
	_, result := PersonaSet{}.Import([]Persona{
		{Name: "拼错了", SystemPrompt: "正文", ReplyStyle: "catgrl"},
		{Name: "中文风格", SystemPrompt: "正文", ReplyStyle: "猫娘"},
		{Name: "也拼错了", SystemPrompt: "正文", ReplyStyle: "catgrl"},
		{Name: "正常的", SystemPrompt: "正文", ReplyStyle: ReplyStyleHuman},
		{Name: "没填风格", SystemPrompt: "正文"},
	}, now)

	if len(result.Imported) != 5 {
		t.Fatalf("认不出风格不该导入失败，只导进来 %d 套", len(result.Imported))
	}
	// 去重：同一个错词写了两遍，提示里只该出现一次。
	if len(result.UnknownStyles) != 2 {
		t.Fatalf("UnknownStyles = %v，应该只有两个去重后的值", result.UnknownStyles)
	}
	for _, want := range []string{"catgrl", "猫娘"} {
		if !slices.Contains(result.UnknownStyles, want) {
			t.Fatalf("没有点名 %q：%v", want, result.UnknownStyles)
		}
	}
	// 合法风格和留空都不该被当成可疑。
	for _, unwanted := range []string{"human", ""} {
		if slices.Contains(result.UnknownStyles, unwanted) {
			t.Fatalf("%q 被误报成认不出来的风格", unwanted)
		}
	}
}

// TestKnownReplyStylesCoversEveryStyle 文档和导入校验共用这张表，漏一档就会把
// 合法风格报成拼写错误。新增风格时忘了加进来，这条会红。
func TestKnownReplyStylesCoversEveryStyle(t *testing.T) {
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise,
		ReplyStyleCatgirl, ReplyStyleRoleplay, ReplyStyleHuman,
	} {
		if !knownReplyStyle(string(style)) {
			t.Fatalf("KnownReplyStyles 漏了 %q", style)
		}
	}
	if knownReplyStyle("definitely-not-a-style") {
		t.Fatal("认不出来的值被当成了合法风格")
	}
}
