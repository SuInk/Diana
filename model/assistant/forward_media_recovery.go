// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// A bridge may retain the original send payload rather than a QQ download URL.
// Refresh that forward's media instead of requesting its placeholder message ID.
func (r *Runtime) refreshForwardMedia(ctx context.Context, event MessageEvent, segments []MessageSegment, resolver LocalMediaPathResolver) []MessageSegment {
	out := append([]MessageSegment(nil), segments...)
	fetched := make(map[string][]MessageSegment)
	ordinals := make(map[string]int)
	for index, segment := range segments {
		id := strings.TrimSpace(segment.Data["forward_id"])
		if id == "" || (segment.Type != "image" && !videoFileSegment(segment)) || segment.Data["source_type"] == "video_frame" {
			continue
		}
		key := id + "\x00" + segment.Type
		ordinal := ordinals[key]
		ordinals[key]++
		if !forwardMediaNeedsRefresh(segment) {
			continue
		}
		media, exists := fetched[id]
		if !exists {
			if len(fetched) >= maxForwardExpandCount {
				continue
			}
			fetched[id] = nil
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			response, err := r.callOneBotAPIForEvent(callCtx, event, "get_forward_msg", map[string]any{"id": id})
			cancel()
			if err != nil {
				r.recordForwardMessageError(ctx, event, id, err)
				continue
			}
			media = forwardMediaSegmentsFromOneBotData(response, id)
			media = resolveSharedImagePaths(media, resolver)
			media, _ = resolveSharedVideoPaths(media, resolver)
			fetched[id] = media
		}
		var candidates []MessageSegment
		for _, candidate := range media {
			if candidate.Type == segment.Type {
				candidates = append(candidates, candidate)
			}
		}
		// Ordinals are safe only within the same immutable forward and media type.
		count := 0
		for _, original := range segments {
			if original.Type == segment.Type && original.Data["forward_id"] == id && original.Data["source_type"] != "video_frame" {
				count++
			}
		}
		if len(candidates) != count || ordinal >= len(candidates) {
			continue
		}
		candidate := candidates[ordinal]
		if !segmentHasMediaSource(candidate) && imageFileToken(candidate.Data) == "" {
			continue
		}
		data := cloneSegmentData(candidate.Data)
		for _, key := range []string{"forward_id", "forward_sender_name", "source_message_id", "source_group_id", "source_user_id"} {
			if data[key] == "" {
				data[key] = segment.Data[key]
			}
		}
		out[index].Data = data
	}
	return out
}

func forwardMediaNeedsRefresh(segment MessageSegment) bool {
	if segment.Data[imageUnavailableKey] == "true" {
		return true
	}
	if !segmentHasMediaSource(segment) {
		return imageFileToken(segment.Data) == ""
	}
	for _, key := range []string{"cached_file", "path", "file"} {
		if usableLocalMediaPath(segment.Data[key]) {
			return false
		}
	}
	for _, key := range []string{"url", "file", "video_url", "image_url", "src"} {
		if isLocalMediaShareURL(segment.Data[key]) {
			return true
		}
	}
	return false
}

func isLocalMediaShareURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.HasPrefix(parsed.Path, "/media/resolver/")
}
