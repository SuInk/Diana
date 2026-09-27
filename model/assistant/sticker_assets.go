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
	// SentCount/LastSentAt 是机器人在查询所在会话里发这张图的记录。
	SentCount  int
	LastSentAt int64
}

type StickerAssetStore interface {
	ListStickerAssets(context.Context, StickerHistoryQuery) ([]StickerAsset, error)
}

// StickerTagRecord 是一张表情包的检索标注，按图片哈希存，跨会话共用。
type StickerTagRecord struct {
	ContentSHA256 string
	Gist          string
	Tags          []string
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
