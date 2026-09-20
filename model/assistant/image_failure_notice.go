// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 识图失败时，提示词里那段「当前图片的独立视觉描述」整块消失，模型手上只剩一句「请
// 分析这张图片」却没有图。它只能自己猜，猜出来的通常是「图没传过来」——把我们这边的
// 故障说成用户没发图。2026-09-21 线上就是这么回复的。
//
// 所以失败要明写进提示词，而且要说清是哪一种失败：这三件事对用户的含义完全不同。
//   - 获取失败：图没下下来或解不开，我们手上根本没有图，让对方重发是对的。
//   - 送不进模型：图我们有、也发出去了，是这条视觉链路不接受图片输入。让对方重发
//     没有任何用，得换模型。
//   - 识别超时：图送到了，模型没在时限内答完。重试有用，重发没用。
const (
	imageFailureUnavailable  = "unavailable"
	imageFailureNotDelivered = "not_delivered"
	imageFailureTimeout      = "timeout"
)

// errVisionImageNotDelivered 表示图片发出去了，但模型那一侧没有收到图片输入。
// 无论是模型本身不支持图片，还是网关把 image part 丢了，对我们都是同一件事：这条
// 链路送不进图，重试同一个模型不会有不同结果。
var errVisionImageNotDelivered = errors.New("vision provider received no image input")

// errVisionImageUnavailable 表示图片内容取不到——文件没了、下载失败或解码失败。
var errVisionImageUnavailable = errors.New("image content is unavailable")

func classifyImageDescriptionFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errVisionImageNotDelivered):
		return imageFailureNotDelivered
	case errors.Is(err, errVisionImageUnavailable):
		return imageFailureUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return imageFailureTimeout
	default:
		return imageFailureTimeout
	}
}

// failurePositions 在没有描述时才需要，把失败原因写回每一处出现该图的片段。
func (t *recallImageTarget) failurePositions() []recallImagePosition {
	if t == nil || t.failure == "" {
		return nil
	}
	return t.positions
}

// imageFailureNotice 把片段上记下的失败原因拼成提示词里的一段话。没有失败记录时返回
// 空串，调用方什么也不加。
func imageFailureNotice(event MessageEvent) string {
	counts := map[string]int{}
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	for _, segment := range segments {
		if !recallStillImageSegment(segment) {
			continue
		}
		if reason := strings.TrimSpace(segment.Data[recallImageFailureKey]); reason != "" {
			counts[reason]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	var lines []string
	// 顺序固定，提示词才不会因为 map 遍历顺序每轮都变，破坏 prompt 缓存。
	for _, reason := range []string{imageFailureNotDelivered, imageFailureUnavailable, imageFailureTimeout} {
		count := counts[reason]
		if count == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %d 张：%s", count, imageFailureExplanations[reason]))
	}
	return "【本轮图片处理失败，用户确实发了图，不要说没收到图片】\n" + strings.Join(lines, "\n")
}

var imageFailureExplanations = map[string]string{
	imageFailureNotDelivered: "图片没能送进视觉模型（这条模型链路不接受图片输入）。让对方重发没有用，据实说明是这边看不了图。",
	imageFailureUnavailable:  "图片内容获取失败（没下到或解不开）。可以请对方重发。",
	imageFailureTimeout:      "图片识别超时，这一轮没看完。可以说稍后再看，不要说没收到。",
}
