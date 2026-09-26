// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 例子取自线上表情包库：同模板换字、同一张图的副本、同一张图描述两次都算同一张；
// 英文里凑巧重叠几个字母、题材相近但画面不同的不算。
func TestStickerSignatureDuplicates(t *testing.T) {
	sign := func(description string) stickerSignature {
		return newStickerSignature(stickerCandidate{Description: description})
	}
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"同模板换字", "少女身前的白色标牌下方印有一行加粗的黑色简体中文文本：“代码在自己上传！”。",
			"猫耳女仆双手欢迎，配醒目的“Z”和文字“代码在自己冒出来！”。适合炫耀开发进展。", true},
		{"原文相同的副本", "一只小猫从桌边探出脑袋，上方写着“小猫探头”。", "低清晰度表情，猫咪只露出眼睛，配字“小猫探头”", true},
		{"同一张图描述两次", "一张白色背景的二次元表情包。画面中央是一名Q版少女角色，灰紫色短发，发丝两侧带青蓝色挑染，双手捂脸害羞",
			"图片为白色背景的表情包插画。中央是一名Q版动漫少女，灰色短发，额前齐刘海，两侧有青蓝色挑染，双手捂脸害羞", true},
		{"英文碰巧重叠", "黑猫趴着睡觉，写着“Sleep”", "被子里露出脑袋，写着“deepsleep”和“深度睡眠中请勿打扰~”", false},
		{"题材相近的不同图", "画面为一张低清晰度表情包式图片。背景中，一只手握着黑色刀柄，竖直举起一把尖头金属刀",
			"一张低清晰度的表情包图片。背景中，一只浅米色猫蜷缩或趴在带红褐色花纹的沙发上", false},
		{"没有简介", "", "", false},
	}
	for _, tc := range cases {
		if got := sign(tc.a).duplicates(sign(tc.b)); got != tc.want {
			t.Errorf("%s: duplicates = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 候选里同一张图的几个副本只留一张，空出来的名额给别的图。
func TestSelectStickerCandidatesSkipsDuplicates(t *testing.T) {
	candidates := []stickerCandidate{
		{ID: "upload", Score: 30, Description: "女仆举着牌子，写着“代码在自己上传！”"},
		{ID: "appear", Score: 20, Description: "女仆举着牌子，写着“代码在自己冒出来！”"},
		{ID: "other", Score: 10, Description: "一只猫翻白眼，配字“无语”"},
	}
	picked, matched := selectStickerCandidates(candidates, 3, 0, func(int) int { return 0 })
	if matched != 2 || len(picked) != 2 || picked[0].ID != "upload" || picked[1].ID != "other" {
		t.Fatalf("picked = %v matched=%d", stickerCandidateIDs(picked), matched)
	}
}

// 刚发过一张，同模板换字的那张也算刚发过，往后排。
func TestRankStickerCandidatesTreatsDuplicateOfSentAsRecent(t *testing.T) {
	now := int64(100000)
	candidates := []stickerCandidate{
		{ID: "sent", Summary: "动画表情", Description: "女仆举牌“代码在自己上传！”", LastSentAt: now - 60},
		{ID: "variant", Summary: "动画表情", Description: "女仆举牌“代码在自己冒出来！”", EventTime: 5},
		{ID: "fresh", Summary: "动画表情", Description: "程序员敲代码敲到冒烟", EventTime: 1},
	}
	rankStickerCandidates(candidates, "代码", now)
	if candidates[0].ID != "fresh" || !candidates[1].RecentlySent || !candidates[2].RecentlySent {
		t.Fatalf("ranking = %v", stickerCandidateIDs(candidates))
	}
}

func stickerCandidateIDs(candidates []stickerCandidate) []string {
	ids := make([]string, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.ID
	}
	return ids
}
