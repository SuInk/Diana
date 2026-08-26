// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 视频是抽了几张关键帧交给模型的，提示词也照实说了——那是为了让它别去臆测没覆盖到
// 的情节和声音。但模型会把这个实现细节原样带进回复：「这帧里是纳西妲主题的等身
// 人偶」。用户发的是一段视频，聊天里没人这么说话。
func TestVideoFrameNarrationRuleKeepsFramesOutOfTheReply(t *testing.T) {
	for _, want := range []string{"视频", "不要出现", "帧"} {
		if !strings.Contains(videoFrameNarrationRule, want) {
			t.Fatalf("措辞规则里缺少 %q：%s", want, videoFrameNarrationRule)
		}
	}
	// 约束的是措辞，不是依据：不能顺手把「只依据画面」也说没了。
	if strings.Contains(videoFrameNarrationRule, "可以推测") {
		t.Fatalf("措辞规则不该放宽依据：%s", videoFrameNarrationRule)
	}
}
