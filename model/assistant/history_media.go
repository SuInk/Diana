// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	historyMediaReadyTimeout = 5 * time.Second
	maxHistoryImageBytes     = 32 << 20
)

const imageContentSHA256Key = "content_sha256"

const imageUnavailableKey = "image_unavailable"

const (
	imageSourceFailedKey   = "image_source_failed"
	imageResolvedSourceKey = "resolved_source_"
)

func cacheMessageEventImages(ctx context.Context, event MessageEvent) MessageEvent {
	event, _ = cacheMessageEventImagesDetailed(ctx, event)
	return event
}

func cacheMessageEventImagesDetailed(ctx context.Context, event MessageEvent) (MessageEvent, []error) {
	var failures []error
	event.Segments, failures = cacheImageSegmentsDetailed(ctx, string(event.Kind), event.GroupID, event.UserID, event.MessageID, event.Segments)
	if event.Quoted != nil {
		quoted := *event.Quoted
		var quotedFailures []error
		quoted.Segments, quotedFailures = cacheImageSegmentsDetailed(ctx, "quoted", firstNonEmpty(quoted.GroupID, event.GroupID), firstNonEmpty(quoted.UserID, event.UserID), quoted.MessageID, quoted.Segments)
		failures = append(failures, quotedFailures...)
		event.Quoted = &quoted
	}
	return event, failures
}

func cacheMessageEventVideos(ctx context.Context, event MessageEvent) MessageEvent {
	event.Segments = cacheVideoFrames(ctx, string(event.Kind), event.GroupID, event.UserID, event.MessageID, event.Segments)
	if event.Quoted != nil {
		quoted := *event.Quoted
		quoted.Segments = cacheVideoFrames(ctx, "quoted", firstNonEmpty(quoted.GroupID, event.GroupID), firstNonEmpty(quoted.UserID, event.UserID), quoted.MessageID, quoted.Segments)
		event.Quoted = &quoted
	}
	return event
}

func cacheVideoFrames(ctx context.Context, targetKind, groupID, userID, messageID string, segments []MessageSegment) []MessageSegment {
	if len(segments) == 0 || hasCachedVideoFrames(segments) {
		return segments
	}
	videoSegments := make([]MessageSegment, 0, 1)
	for _, segment := range segments {
		if segment.Type == "video" && len(videoSourceCandidates([]MessageSegment{segment})) > 0 {
			videoSegments = append(videoSegments, segment)
		}
	}
	if len(videoSegments) == 0 {
		// QQ 也会把 mp4 作为 file 段投递。它没有合并转发节点元数据，但仍要维持
		// 原有的视频理解能力；按解析出的每个源构造独立媒体项即可。
		for _, source := range videoSourceCandidates(segments) {
			videoSegments = append(videoSegments, MessageSegment{Type: "video", Data: map[string]string{"url": source}})
		}
		if len(videoSegments) == 0 {
			return segments
		}
	}
	out := append([]MessageSegment(nil), segments...)
	frameCount := 0
	for videoIndex, video := range videoSegments {
		videoURLs := videoSourceCandidates([]MessageSegment{video})
		frames := extractVideoContextFramesAfterReady(ctx, videoURLs, historyMediaReadyTimeout)
		defer cleanupVideoContextFrames(frames)
		for frameIndex, frame := range frames {
			if frameCount >= maxVideoContextFrames {
				break
			}
			body, err := os.ReadFile(frame)
			if err != nil {
				continue
			}
			source := fmt.Sprintf("video-frame:%d:%d:%s", videoIndex, frameIndex, firstNonEmpty(videoURLs...))
			path, err := writeHistoryImage(targetKind, groupID, userID, messageID, source, body, "image/jpeg")
			if err != nil {
				continue
			}
			data := map[string]string{
				"cached_file":         path,
				"cached_mime":         "image/jpeg",
				"cached_size":         fmt.Sprint(len(body)),
				imageContentSHA256Key: imageBytesSHA256(body),
				"source_type":         "video_frame",
				"frame_index":         fmt.Sprint(frameIndex),
				"video_index":         fmt.Sprint(videoIndex),
			}
			for _, key := range []string{"forward_id", "source_message_id", "source_group_id", "source_user_id", "forward_sender_name"} {
				if value := strings.TrimSpace(video.Data[key]); value != "" {
					data[key] = value
				}
			}
			out = append(out, MessageSegment{Type: "image", Data: data})
			frameCount++
		}
		if frameCount >= maxVideoContextFrames {
			break
		}
	}
	if frameCount == 0 {
		log.Printf("chatbot video history cache produced no frames: message_id=%s", messageID)
		return segments
	}
	log.Printf("chatbot video history cached: message_id=%s frames=%d", messageID, len(cachedVideoFrameURLs(out)))
	return out
}

func hasCachedVideoFrames(segments []MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == "image" && segment.Data["source_type"] == "video_frame" && strings.TrimSpace(segment.Data["cached_file"]) != "" {
			return true
		}
	}
	return false
}

func cachedVideoFrameURLs(segments []MessageSegment) []string {
	out := make([]string, 0, 4)
	for _, segment := range segments {
		if segment.Type != "image" || segment.Data["source_type"] != "video_frame" {
			continue
		}
		if path := normalizedImageURL(segment.Data["cached_file"]); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func cacheImageSegments(ctx context.Context, targetKind, groupID, userID, messageID string, segments []MessageSegment) []MessageSegment {
	out, _ := cacheImageSegmentsDetailed(ctx, targetKind, groupID, userID, messageID, segments)
	return out
}

func cacheImageSegmentsDetailed(ctx context.Context, targetKind, groupID, userID, messageID string, segments []MessageSegment) ([]MessageSegment, []error) {
	if len(segments) == 0 {
		return segments, nil
	}
	out := make([]MessageSegment, len(segments))
	copy(out, segments)
	var failures []error
	for i, segment := range out {
		if segment.Type != "image" {
			continue
		}
		data := cloneSegmentData(segment.Data)
		if cached := normalizedLocalImagePath(segment.Data["cached_file"]); cached != "" {
			if body, _, err := readHistoryImageSource(ctx, cached, 0); err == nil {
				delete(data, imageUnavailableKey)
				delete(data, imageSourceFailedKey)
				if data[imageContentSHA256Key] == "" {
					data[imageContentSHA256Key] = imageBytesSHA256(body)
				}
				out[i].Data = data
				continue
			}
		}
		if imageSegmentHasInlineData(segment) {
			delete(data, imageUnavailableKey)
			delete(data, imageSourceFailedKey)
			out[i].Data = data
			continue
		}
		var sourceErrors []error
		cached := false
		for _, source := range imageSourceCandidates(segment) {
			body, contentType, err := readHistoryImageSource(ctx, source, historyMediaReadyTimeout)
			if err != nil {
				sourceErrors = append(sourceErrors, err)
				continue
			}
			path, err := writeHistoryImage(targetKind, groupID, userID, messageID, source, body, contentType)
			if err != nil {
				sourceErrors = append(sourceErrors, err)
				continue
			}
			data["cached_file"] = path
			data["cached_mime"] = contentType
			data["cached_size"] = fmt.Sprint(len(body))
			data[imageContentSHA256Key] = imageBytesSHA256(body)
			delete(data, imageUnavailableKey)
			delete(data, imageSourceFailedKey)
			cached = true
			break
		}
		if !cached {
			data[imageUnavailableKey] = "true"
			data[imageSourceFailedKey] = "true"
			cause := errors.Join(sourceErrors...)
			if cause == nil {
				cause = fmt.Errorf("image source is unavailable")
			}
			failures = append(failures, fmt.Errorf("message %s image %d: %w", firstNonEmpty(messageID, "unknown"), i+1, cause))
		}
		out[i].Data = data
	}
	return out, failures
}

func imageSegmentHasInlineData(segment MessageSegment) bool {
	for _, key := range []string{"cached_file", "url", "image_url", "src", "file", "path"} {
		if inlineImageSource(segment.Data[key]) {
			return true
		}
	}
	return false
}

func persistInlineImageSegments(targetKind, groupID, userID, messageID string, segments []MessageSegment) ([]MessageSegment, []error) {
	out := append([]MessageSegment(nil), segments...)
	var failures []error
	for index, segment := range out {
		if segment.Type != "image" || normalizedLocalImagePath(segment.Data["cached_file"]) != "" {
			continue
		}
		source := ""
		for _, key := range []string{"url", "image_url", "src", "file", "path"} {
			if inlineImageSource(segment.Data[key]) {
				source = strings.TrimSpace(segment.Data[key])
				break
			}
		}
		if source == "" {
			continue
		}
		body, contentType, err := decodeInlineHistoryImage(source)
		if err == nil {
			var path string
			path, err = writeHistoryImage(targetKind, groupID, userID, messageID, source, body, contentType)
			if err == nil {
				data := cloneSegmentData(segment.Data)
				data["cached_file"] = path
				data["cached_mime"] = contentType
				data["cached_size"] = fmt.Sprint(len(body))
				data[imageContentSHA256Key] = imageBytesSHA256(body)
				delete(data, imageUnavailableKey)
				delete(data, imageSourceFailedKey)
				out[index].Data = data
				continue
			}
		}
		failures = append(failures, fmt.Errorf("message %s image %d: persist inline image: %w", firstNonEmpty(messageID, "unknown"), index+1, err))
	}
	return out, failures
}

func imageSourceCandidates(segment MessageSegment) []string {
	var out []string
	seen := map[string]struct{}{}
	// Prefer the newest NapCat resolution. An older entry is commonly the same
	// expired-rkey URL that triggered the fallback and may otherwise cost 8s again.
	for index := 8; index >= 1; index-- {
		value := strings.TrimSpace(segment.Data[fmt.Sprintf("%s%d", imageResolvedSourceKey, index)])
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) > 0 || strings.EqualFold(strings.TrimSpace(segment.Data[imageSourceFailedKey]), "true") {
		return out
	}
	for _, key := range []string{"cached_file", "sourcePath", "source_path", "filePath", "file_path", "path", "file", "url", "image_url", "src"} {
		value := strings.TrimSpace(segment.Data[key])
		if normalizedHTTPURL(value) == "" && rawAbsoluteMediaPath(value) == "" && !inlineImageSource(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func availableImageURLs(segments []MessageSegment) []string {
	available := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type == "image" && strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
			continue
		}
		available = append(available, segment)
	}
	return ImageURLs(available)
}

func resolveSharedImagePaths(segments []MessageSegment, resolver LocalMediaPathResolver) []MessageSegment {
	if resolver == nil || len(segments) == 0 {
		return segments
	}
	out := append([]MessageSegment(nil), segments...)
	for index, segment := range out {
		if segment.Type != "image" {
			continue
		}
		data := cloneSegmentData(segment.Data)
		for _, key := range []string{"url", "image_url", "src", "file"} {
			path, ok := resolver.ResolveSharedPath(data[key])
			if !ok {
				continue
			}
			appendResolvedImageSource(data, path, "path")
			break
		}
		out[index].Data = data
	}
	return out
}

func resolveSharedVideoPaths(segments []MessageSegment, resolver LocalMediaPathResolver) ([]MessageSegment, bool) {
	if resolver == nil || len(segments) == 0 {
		return segments, false
	}
	out := append([]MessageSegment(nil), segments...)
	resolved := false
	for index, segment := range out {
		if segment.Type != "video" {
			continue
		}
		data := cloneSegmentData(segment.Data)
		for _, key := range []string{"file", "url", "video_url", "src"} {
			path, ok := resolver.ResolveSharedPath(data[key])
			if !ok {
				continue
			}
			data[key] = path
			resolved = true
			break
		}
		out[index].Data = data
	}
	return out, resolved
}

func imageBytesSHA256(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func imageSegmentContentSHA256(segment MessageSegment) (string, bool) {
	if segment.Type != "image" {
		return "", false
	}
	if value := strings.ToLower(strings.TrimSpace(segment.Data[imageContentSHA256Key])); validSHA256(value) {
		return value, true
	}
	path := normalizedLocalImagePath(segment.Data["cached_file"])
	if path == "" {
		return "", false
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 {
		return "", false
	}
	return imageBytesSHA256(body), true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func firstRemoteImageURL(segment MessageSegment) string {
	for _, key := range []string{"url", "image_url", "src", "file"} {
		if value := normalizedHTTPURL(segment.Data[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstImageSource(segment MessageSegment) string {
	for _, key := range []string{"cached_file", "sourcePath", "source_path", "filePath", "file_path", "path", "url", "image_url", "src", "file"} {
		value := strings.TrimSpace(segment.Data[key])
		if normalizedHTTPURL(value) != "" || rawAbsoluteMediaPath(value) != "" || inlineImageSource(value) {
			return value
		}
	}
	return ""
}

func readHistoryImageSource(ctx context.Context, source string, wait time.Duration) ([]byte, string, error) {
	if inlineImageSource(source) {
		return decodeInlineHistoryImage(source)
	}
	if remote := normalizedHTTPURL(source); remote != "" {
		return downloadImageBytesWithLimit(ctx, remote, maxHistoryImageBytes)
	}
	path := waitForLocalMediaPath(ctx, source, wait, maxHistoryImageBytes)
	if path == "" {
		return nil, "", fmt.Errorf("image source is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxHistoryImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 || len(body) > maxHistoryImageBytes {
		return nil, "", fmt.Errorf("image size is invalid")
	}
	contentType := imageContentType(http.DetectContentType(body), body)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("local content is not an image")
	}
	return body, contentType, nil
}

// ReadMessageImageSegment resolves a persisted OneBot image segment into
// validated image bytes. Callers receive no filesystem path or temporary
// source URL, which makes it suitable for authenticated WebUI previews.
func ReadMessageImageSegment(ctx context.Context, segment MessageSegment) ([]byte, string, error) {
	if segment.Type != "image" {
		return nil, "", fmt.Errorf("message segment is not an image")
	}
	seen := map[string]struct{}{}
	sources := make([]string, 0, 10)
	appendSource := func(source string) {
		source = strings.TrimSpace(source)
		if source == "" {
			return
		}
		if _, found := seen[source]; found {
			return
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	// Keep a durable local cache ahead of every OneBot URL. This also allows
	// old rows with a stale failure marker to use a cache written later.
	appendSource(segment.Data["cached_file"])
	for _, source := range imageSourceCandidates(segment) {
		appendSource(source)
	}
	var failures []error
	for _, source := range sources {
		body, contentType, err := readHistoryImageSource(ctx, source, 0)
		if err == nil {
			return body, contentType, nil
		}
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return nil, "", err
	}
	return nil, "", fmt.Errorf("image source is unavailable")
}

func inlineImageSource(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "data:image/") || strings.HasPrefix(value, "base64://")
}

func decodeInlineHistoryImage(source string) ([]byte, string, error) {
	source = strings.TrimSpace(source)
	contentType := "image/jpeg"
	payload := ""
	if strings.HasPrefix(source, "base64://") {
		payload = strings.TrimSpace(strings.TrimPrefix(source, "base64://"))
	} else {
		header, encoded, ok := strings.Cut(source, ",")
		if !ok || !strings.HasPrefix(header, "data:image/") || !strings.Contains(strings.ToLower(header), ";base64") {
			return nil, "", fmt.Errorf("unsupported inline image")
		}
		contentType = strings.TrimPrefix(strings.SplitN(header, ";", 2)[0], "data:")
		payload = strings.TrimSpace(encoded)
	}
	if payload == "" {
		return nil, "", fmt.Errorf("inline image is empty")
	}
	body, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		body, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil, "", fmt.Errorf("decode inline image: %w", err)
	}
	if len(body) == 0 || len(body) > maxHistoryImageBytes {
		return nil, "", fmt.Errorf("image size is invalid")
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return nil, "", fmt.Errorf("inline content is not an image")
	}
	return body, contentType, nil
}

func writeHistoryImage(targetKind, groupID, userID, messageID, source string, body []byte, contentType string) (string, error) {
	baseDir, err := historyMediaDir()
	if err != nil {
		return "", err
	}
	session := historyMediaSession(targetKind, groupID, userID)
	messageID = safeHistoryPart(firstNonEmpty(messageID, "no-message"))
	hash := sha256.Sum256([]byte(session + ":" + messageID + ":" + source))
	name := hex.EncodeToString(hash[:])[:16] + imageExtension(contentType, body)
	dir := filepath.Join(baseDir, session, messageID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func historyMediaDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv("DIANA_HISTORY_MEDIA_DIR")); value != "" {
		return value, nil
	}
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "history-media"), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "diana", "history-media"), nil
}

func historyMediaSession(targetKind, groupID, userID string) string {
	if targetKind == string(EventKindGroup) || groupID != "" {
		return "group_" + safeHistoryPart(firstNonEmpty(groupID, "unknown"))
	}
	return "private_" + safeHistoryPart(firstNonEmpty(userID, "unknown"))
}

func safeHistoryPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func imageExtension(contentType string, body []byte) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	if len(body) >= 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "WEBP" {
		return ".webp"
	}
	return ".img"
}
