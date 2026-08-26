// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
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

// 读不到视频时原先只写一句「读取或抽帧失败」，模型只能照着复述，用户不知道是这台
// 机器没装 ffmpeg 还是视频太大。原因就在手边，不该丢掉。
func TestVideoContextFailureNamesTheReason(t *testing.T) {
	// 没装 ffmpeg 是最常见的一种，要能一眼看出是部署环境的问题。
	if _, reason := extractVideoContextFramesDetailed(t.Context(), nil, 0); reason != "" {
		t.Fatalf("没有视频源时不该报错：%q", reason)
	}

	// 本地文件不存在：报的是「没就绪或超上限」，不是笼统的失败。
	frames, reason := extractVideoContextFramesDetailed(t.Context(), []string{"/nonexistent/diana-test-video.mp4"}, 0)
	if len(frames) != 0 {
		t.Fatalf("不存在的文件不该抽出画面：%#v", frames)
	}
	if reason == "" {
		t.Fatal("失败必须给出原因")
	}
	// 原因是给用户看的，不该出现内部实现词。
	for _, leaked := range []string{"抽帧", "关键帧"} {
		if strings.Contains(reason, leaked) {
			t.Fatalf("失败原因泄漏了实现细节 %q：%s", leaked, reason)
		}
	}
}

// 大小上限要报出具体数字，「太大了」对排查没有帮助。
func TestVideoContextErrorNamesTheSizeLimit(t *testing.T) {
	got := describeVideoContextError(errVideoContextTestOversize, 100*1024*1024)
	if !strings.Contains(got, "100 MB") {
		t.Fatalf("超限文案里没有具体上限：%s", got)
	}
}

var errVideoContextTestOversize = fmt.Errorf("video exceeds file parser limit: %d > %d bytes", 200, 100)
