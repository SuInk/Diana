// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 排队等着识图的任务不能攥着图片本体。
//
// 视觉识别是单并发（historyImageDescSem 容量 1），但排队用的是「先起 goroutine，
// 再在 goroutine 里等信号量」——最多 32 个（historyImageDescriptionQueueLimit）。
// 于是有 31 个 goroutine 纯粹在等，一点吞吐都不换，却各自钉住一整条消息。
//
// 钉住的东西不小：
//   - jobEvent 里那一段图片 segment，内联图的 Data 直接装着 base64
//   - indexEvent 是整条源消息，一条多图消息就是好几份
//   - base64 比二进制大三分之一，Go 的 map/string 拷贝之后还可能各留一份
//
// 启动后的历史回填几十秒内就能碰到 32 张没描述过的图，队列瞬间填满——进程在
// 识完第一张之前就被 OOM 杀掉了。
//
// 关键在于：下游一样都不需要图片本体。
//   - imageSegmentContentSHA256 认 Data[content_sha256]，认不到就去读 cached_file 那个本地路径
//   - firstImageSource 也是优先取 cached_file 这类路径
//   - 真正的解码在 describeRecallImage → llmReadyImageURLs，那已经在信号量之后了
//
// 所以入队前把 segment 削成「哈希 + 本地路径」，等轮到自己再从路径读字节。
// 削完一条任务是几百字节，32 个排队任务加起来还不到 100 KiB。

// queuedImageSegmentKeys 是削剩下的键：一个用来算哈希，其余用来定位图片本体。
// 内联 base64 会出现在 file / url / src 这些键上，一概不留。
var queuedImageSegmentKeys = []string{
	imageContentSHA256Key,
	"cached_file",
	"sourcePath",
	"source_path",
	"filePath",
	"file_path",
	"path",
}

// stripImageSegmentForQueue 把图片段削到只剩定位信息。
//
// 只保留路径类的键：路径永远是短字符串，而 file / url / src 上可能挂着整张图的
// base64。哈希算不出来或者路径没留下的段，调用方本来就会跳过。
func stripImageSegmentForQueue(segment MessageSegment) MessageSegment {
	stripped := MessageSegment{Type: segment.Type, Data: map[string]string{}}
	if hash, ok := imageSegmentContentSHA256(segment); ok {
		stripped.Data[imageContentSHA256Key] = hash
	}
	for _, key := range queuedImageSegmentKeys {
		if key == imageContentSHA256Key {
			continue
		}
		if value := strings.TrimSpace(segment.Data[key]); value != "" {
			stripped.Data[key] = value
		}
	}
	return stripped
}

// queuedImageSourceRetained 判断入队后还能不能定位到这张图。
//
// 削完只剩路径。图片只以内联 base64 存在时削完就没了来源——这种任务不入队，
// 留着队列里那份 base64 才是 OOM 的来源。它仍会在真正被引用、走完整路径时描述。
func queuedImageSourceRetained(segment MessageSegment) (string, bool) {
	source := firstImageSource(stripImageSegmentForQueue(segment))
	return source, source != ""
}

// historyImageDescriptionQueueEvent 把消息削成排队用的轻量副本。
//
// 图片段只留定位信息，文本段原样留着（检索文本要用），其余带体积的段丢掉。
// Quoted 单独处理：historyImageDescriptionEvents 已经把引用消息拆成了独立事件，
// 这里再挂一份就是重复钉住。
func historyImageDescriptionQueueEvent(event MessageEvent) MessageEvent {
	queued := event
	queued.Quoted = nil
	segments := make([]MessageSegment, 0, len(event.Segments))
	for _, segment := range event.Segments {
		switch {
		case segment.Type == "image":
			segments = append(segments, stripImageSegmentForQueue(segment))
		case segment.Type == "text":
			segments = append(segments, segment)
		}
	}
	queued.Segments = segments
	return queued
}
