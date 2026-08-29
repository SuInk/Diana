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
}

type StickerAssetStore interface {
	ListStickerAssets(context.Context, StickerHistoryQuery) ([]StickerAsset, error)
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
