// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
)

// 图片开场白（「开始生成图片，完成后我会把结果发出来」）在一轮里只能出现一次。
//
// 它当初是运行时在工具调用当场直接发出去的，为的是堵住模型改口说「我做不到」
// 「你没有权限」——任务其实已经在后台跑了，用户那边却只看到一句推脱。代价是：
// 模型随后的 final 回复照样会说一遍图片的事，用户连着收到两条几乎一样的话，
// 而用户消息本身如果只是「生成图片」，模型也没有别的内容可回。
//
// 改成先攒着：工具把开场白交给本轮回复，模型自己说了就用模型那句，模型什么都
// 没说才把它作为这一轮的回复发出去。任何一种情况下都恰好一条文字。
//
// 拿不到 sink（不在回复轮次里，例如后台任务直接调工具）时退回原来的立即发送，
// 那些场景本来就没有 final 回复来兜底。
type imageAnnouncementSink struct {
	mu   sync.Mutex
	text string
}

type imageAnnouncementSinkKey struct{}

func withImageAnnouncementSink(ctx context.Context) (context.Context, *imageAnnouncementSink) {
	sink := &imageAnnouncementSink{}
	return context.WithValue(ctx, imageAnnouncementSinkKey{}, sink), sink
}

func imageAnnouncementSinkFrom(ctx context.Context) *imageAnnouncementSink {
	sink, _ := ctx.Value(imageAnnouncementSinkKey{}).(*imageAnnouncementSink)
	return sink
}

// offer 记下开场白。一轮里只留第一条：同一轮生成多张图时，用户要的是一句
// 「在画了」，不是每张一句。
func (s *imageAnnouncementSink) offer(text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.text == "" {
		s.text = text
	}
}

// drain 取走开场白并清空。
func (s *imageAnnouncementSink) drain() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.text
	s.text = ""
	return text
}

// drainPendingImageAnnouncement 取走本轮攒下的开场白，供空回复时兜底。
func drainPendingImageAnnouncement(ctx context.Context) string {
	return imageAnnouncementSinkFrom(ctx).drain()
}
