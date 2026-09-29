// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
)

// StickerAsset is a durable library entry. The media file remains in Diana's
// history-media store; this index decouples discovery from chat history windows.
type StickerAsset struct {
	Session          string
	ProfileID        string
	ContextNamespace string
	Kind             EventKind
	GroupID          string
	UserID           string
	MessageID        string
	EventTime        int64
	SegmentIndex     int
	Summary          string
	Path             string
	MIME             string
	ContentSHA256    string
	// Description 是这张图的通用图片描述，Gist/Tags 是表情包专用标注；Tagged 表示标注过。
	Description string
	Gist        string
	Tags        []string
	Tagged      bool
	// Category 是画风大类；CategoryKnown 表示判过，判不出来时 Category 为空。
	Category      string
	CategoryKnown bool
	// SentCount/LastSentAt 是机器人在查询所在会话里发这张图的记录。
	SentCount  int
	LastSentAt int64
	// ElsewhereLastSentAt 是机器人最近一次在别的会话里发这张图，跨会话共享时用来降权。
	ElsewhereLastSentAt int64
}

type StickerAssetStore interface {
	ListStickerAssets(context.Context, StickerHistoryQuery) ([]StickerAsset, error)
}

// StickerAnnotationVersion 标记表情包标注是按哪种看图方式做的。frames-v1：动图按多帧分镜看
// （见 gif_storyboard.go）。之前的 GIF 标注只看了第一帧，版本不符的 GIF 标注按没标注处理。
const StickerAnnotationVersion = "frames-v1"

const stickerAnnotationVersion = StickerAnnotationVersion

// StickerTagRecord 是一张表情包的检索标注，按图片哈希存，跨会话共用。
type StickerTagRecord struct {
	ContentSHA256 string
	Gist          string
	Tags          []string
	// Category 是 StickerCategories 里的一个画风大类，判不出来为空。
	Category string
	Version  string
}

// StickerCategories 是表情包的画风大类，控制台按它分类，标注提示词让模型从里面选一个。
var StickerCategories = []string{"二次元", "真人", "动物", "文字", "其他"}

// NormalizeStickerCategory 把模型写的大类收敛到 StickerCategories，常见的近义说法也认。
func NormalizeStickerCategory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, category := range StickerCategories {
		if value == category {
			return category
		}
	}
	for category, aliases := range map[string][]string{
		"二次元": {"动漫", "动画", "漫画", "卡通", "游戏", "插画", "ACG", "acg"},
		"真人":  {"人物", "照片", "明星", "影视", "三次元"},
		"动物":  {"猫", "狗", "宠物"},
		"文字":  {"纯文字", "字"},
	} {
		for _, alias := range aliases {
			if strings.Contains(value, alias) {
				return category
			}
		}
	}
	return "其他"
}

type StickerTagStore interface {
	SaveStickerTags(context.Context, StickerTagRecord) error
}

type StickerUsageStore interface {
	RecordStickerSent(ctx context.Context, session, contentSHA256 string, sentAt int64) error
}

// StickerLibraryPruner 把一个会话的表情包库压到上限以内，返回淘汰了几张。
type StickerLibraryPruner interface {
	PruneStickerAssets(ctx context.Context, session string, capacity int) (int, error)
}

// eventHasSticker 判断这条消息会不会往表情包库里新增条目。
func eventHasSticker(event MessageEvent) bool {
	for _, segment := range event.Segments {
		if _, ok := StickerSegmentLabel(segment); ok {
			return true
		}
	}
	return false
}

// StickerSegmentLabel recognizes platform sticker metadata while rejecting
// ordinary images. Named summaries are retained; unnamed platform stickers get
// a stable generic label that callers may choose to exclude.
func StickerSegmentLabel(segment MessageSegment) (string, bool) {
	if segment.Type != "image" {
		return "", false
	}
	summary := normalizeStickerSummary(segment.Data["summary"])
	platformSticker := false
	if subType := strings.TrimSpace(segment.Data["sub_type"]); subType != "" && subType != "0" {
		platformSticker = true
	}
	for _, key := range []string{"emoji_id", "emoji_package_id", "emoji_type"} {
		if strings.TrimSpace(segment.Data[key]) != "" {
			platformSticker = true
			break
		}
	}
	if summary != "" && summary != "图片" {
		return summary, true
	}
	if platformSticker {
		return "动画表情", true
	}
	return "", false
}
