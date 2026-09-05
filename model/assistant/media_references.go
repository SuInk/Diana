// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var videoMediaExtensions = map[string]struct{}{
	".3gp": {}, ".avi": {}, ".flv": {}, ".m4v": {}, ".mkv": {},
	".mov": {}, ".mp4": {}, ".mpeg": {}, ".mpg": {}, ".ts": {}, ".webm": {},
}

func (r *Runtime) enrichMediaReferences(ctx context.Context, event MessageEvent) MessageEvent {
	event, _ = r.enrichMediaReferencesDetailed(ctx, event)
	return event
}

func (r *Runtime) enrichMediaReferencesDetailed(ctx context.Context, event MessageEvent) (MessageEvent, []error) {
	if r.channel == nil {
		return event, nil
	}
	var failures []error
	event.Segments, failures = r.enrichMediaSegmentsDetailed(ctx, event, event.Segments)
	if event.Quoted != nil {
		quoted := *event.Quoted
		quotedEvent := event
		quotedEvent.GroupID = firstNonEmpty(quoted.GroupID, event.GroupID)
		quotedEvent.MessageID = quoted.MessageID
		var quotedFailures []error
		quoted.Segments, quotedFailures = r.enrichMediaSegmentsDetailed(ctx, quotedEvent, quoted.Segments)
		failures = append(failures, quotedFailures...)
		event.Quoted = &quoted
	}
	return event, failures
}

func (r *Runtime) enrichMediaSegmentsDetailed(ctx context.Context, event MessageEvent, segments []MessageSegment) ([]MessageSegment, []error) {
	r.mu.RLock()
	resolver, _ := r.localMedia.(LocalMediaPathResolver)
	r.mu.RUnlock()
	segments = resolveSharedImagePaths(segments, resolver)
	segments, _ = resolveSharedVideoPaths(segments, resolver)
	segments = r.refreshForwardMedia(ctx, event, segments, resolver)
	out := append([]MessageSegment(nil), segments...)
	var failures []error
	for index, segment := range out {
		if segment.Type != "image" && segment.Type != "record" && !videoFileSegment(segment) {
			continue
		}
		if segment.Type == "image" {
			needsFallback := strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") ||
				(segment.Data["forward_id"] != "" && forwardMediaNeedsRefresh(segment))
			if !needsFallback && segmentHasMediaSource(segment) {
				continue
			}
		} else if segmentHasMediaSource(segment) && !(segment.Data["forward_id"] != "" && forwardMediaNeedsRefresh(segment)) {
			continue
		}
		data := cloneSegmentData(segment.Data)
		sourceGroupID := firstNonEmpty(data["source_group_id"], event.GroupID)
		sourceMessageIDs := usableOneBotMessageIDs(data["source_message_id"], event.MessageID)
		if data["forward_id"] != "" {
			// The outer message identifies the card, not a media item in its nodes.
			sourceMessageIDs = usableOneBotMessageIDs(data["source_message_id"])
		}
		var requests []oneBotFileResolveRequest
		if segment.Type == "image" {
			file := imageFileToken(data)
			if file != "" && resolvedImageSourceCount(data) == 0 {
				requests = append(requests, oneBotFileResolveRequest{action: "get_image", params: map[string]any{"file": file}})
			}
			for _, sourceMessageID := range sourceMessageIDs {
				requests = append(requests, oneBotFileResolveRequest{action: "get_msg", params: map[string]any{"message_id": oneBotMessageIDParam(sourceMessageID)}})
			}
		} else if segment.Type == "record" {
			token := firstNonEmpty(data["file"], data["file_id"], data["id"])
			if token != "" {
				requests = append(requests, oneBotFileResolveRequest{action: "get_record", params: map[string]any{"file": token, "out_format": "wav"}})
			}
			for _, sourceMessageID := range sourceMessageIDs {
				requests = append(requests, oneBotFileResolveRequest{action: "get_msg", params: map[string]any{"message_id": oneBotMessageIDParam(sourceMessageID)}})
			}
		} else {
			ref := fileRef{
				Name:    mediaSegmentName(segment),
				FileID:  firstNonEmpty(data["file_id"], data["id"], data["fid"], data["file"]),
				BusID:   firstNonEmpty(data["busid"], data["bus_id"]),
				GroupID: sourceGroupID,
			}
			if segment.Type == "video" {
				if token := firstNonEmpty(ref.FileID, ref.Name); token != "" {
					requests = []oneBotFileResolveRequest{{action: "get_file", params: map[string]any{"file": token}}}
				}
			} else {
				requests = oneBotFileResolveRequests(ref)
			}
			for _, sourceMessageID := range sourceMessageIDs {
				requests = append(requests, oneBotFileResolveRequest{
					action: "get_msg",
					params: map[string]any{"message_id": oneBotMessageIDParam(sourceMessageID)},
				})
			}
		}
		if len(requests) == 0 {
			if segment.Type == "image" {
				failures = append(failures, fmt.Errorf("message %s image %d: OneBot get_image has no file token", firstNonEmpty(event.MessageID, "unknown"), index+1))
			}
			continue
		}
		timeout := 8 * time.Second
		if videoFileSegment(segment) {
			timeout = 60 * time.Second
			if data["forward_id"] != "" {
				timeout = 20 * time.Second
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		var resolutionErrors []error
		resolvedImageSource := false
		directImageToken := ""
		for _, request := range requests {
			response, err := r.callOneBotAPIForEvent(callCtx, event, request.action, request.params)
			if err != nil {
				resolutionErrors = append(resolutionErrors, fmt.Errorf("%s: %w", request.action, err))
				continue
			}
			if segment.Type == "image" {
				if request.action == "get_image" {
					directImageToken = strings.TrimSpace(stringFromAny(request.params["file"]))
				}
				if request.action == "get_msg" {
					token := firstNonEmpty(mediaFileTokenFromOneBotData(response, segment), imageFileToken(data))
					if token != "" && (!resolvedImageSource || token != directImageToken) {
						resolved, resolveErr := r.callOneBotAPIForEvent(callCtx, event, "get_image", map[string]any{"file": token})
						if resolveErr != nil {
							resolutionErrors = append(resolutionErrors, fmt.Errorf("get_image after get_msg: %w", resolveErr))
						} else if source, key := mediaSourceFromOneBotData(resolved, segment); source != "" {
							resolvedImageSource = appendResolvedImageSource(data, source, key) || resolvedImageSource
						}
					}
				}
				if source, key := mediaSourceFromOneBotData(response, segment); source != "" {
					resolvedImageSource = appendResolvedImageSource(data, source, key) || resolvedImageSource
				}
				if resolvedImageSource {
					break
				}
				continue
			}
			if source, key := mediaSourceFromOneBotData(response, segment); source != "" {
				data[key] = source
				break
			}
			if request.action != "get_msg" {
				continue
			}
			// NapCat registers incoming message media in an in-memory map while
			// converting get_msg. Retry get_file after that conversion, even when
			// the exposed token is the original filename.
			token := firstNonEmpty(mediaFileTokenFromOneBotData(response, segment), data["file"], data["file_id"])
			if token == "" {
				continue
			}
			action := "get_file"
			params := map[string]any{"file": token}
			if segment.Type == "record" {
				action = "get_record"
				params["out_format"] = "wav"
			}
			resolved, resolveErr := r.callOneBotAPIForEvent(callCtx, event, action, params)
			if resolveErr != nil {
				continue
			}
			if source, key := mediaSourceFromOneBotData(resolved, segment); source != "" {
				data[key] = source
				break
			}
		}
		cancel()
		if segment.Type == "image" && !resolvedImageSource {
			cause := errors.Join(resolutionErrors...)
			if cause == nil {
				cause = fmt.Errorf("OneBot get_image returned no readable source")
			}
			failures = append(failures, fmt.Errorf("message %s image %d: %w", firstNonEmpty(event.MessageID, "unknown"), index+1, cause))
		}
		out[index].Data = data
	}
	return out, failures
}

func appendResolvedImageSource(data map[string]string, source, sourceKey string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	if sourceKey == "url" || sourceKey == "path" {
		data[sourceKey] = source
	}
	for index := 1; index <= 8; index++ {
		key := fmt.Sprintf("%s%d", imageResolvedSourceKey, index)
		if data[key] == source {
			return true
		}
		if strings.TrimSpace(data[key]) == "" {
			data[key] = source
			return true
		}
	}
	return false
}

func resolvedImageSourceCount(data map[string]string) int {
	count := 0
	for index := 1; index <= 8; index++ {
		if strings.TrimSpace(data[fmt.Sprintf("%s%d", imageResolvedSourceKey, index)]) != "" {
			count++
		}
	}
	return count
}

func uniqueNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
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

func mediaFileTokenFromOneBotData(data map[string]any, target MessageSegment) string {
	if token := mediaFileTokenFromOneBotValue(data, target); token != "" {
		return token
	}
	for _, key := range oneBotMediaContainerKeys() {
		if token := mediaFileTokenFromOneBotValue(data[key], target); token != "" {
			return token
		}
	}
	return ""
}

func mediaFileTokenFromOneBotValue(value any, target MessageSegment) string {
	switch item := value.(type) {
	case []any:
		for _, entry := range item {
			if token := mediaFileTokenFromOneBotValue(entry, target); token != "" {
				return token
			}
		}
	case []map[string]any:
		for _, entry := range item {
			if token := mediaFileTokenFromOneBotValue(entry, target); token != "" {
				return token
			}
		}
	case map[string]any:
		segmentType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
		if segmentType == "image" || segmentType == "video" || segmentType == "file" || segmentType == "record" {
			if segmentType != target.Type {
				return ""
			}
			if segmentData, ok := item["data"].(map[string]any); ok {
				if mediaSegmentMatchesAny(target, segmentData) {
					return imageFileTokenAny(segmentData)
				}
				return ""
			}
		}
		if token := stableImageFileTokenAny(item); token != "" && mediaSegmentMatchesAny(target, item) {
			return token
		}
		for _, key := range oneBotMediaContainerKeys() {
			if token := mediaFileTokenFromOneBotValue(item[key], target); token != "" {
				return token
			}
		}
	}
	return ""
}

func mediaSourceFromOneBotData(data map[string]any, target MessageSegment) (string, string) {
	if segmentType := strings.ToLower(strings.TrimSpace(stringFromAny(data["type"]))); segmentType == "image" || segmentType == "video" || segmentType == "file" || segmentType == "record" {
		if segmentType != target.Type {
			return "", ""
		}
		if segmentData, ok := data["data"].(map[string]any); ok {
			if !mediaSegmentMatchesAny(target, segmentData) {
				return "", ""
			}
			return mediaSourceFromOneBotData(segmentData, target)
		}
	}
	keys := []string{"url", "download_url", "file_url", "video_url", "path", "file_path", "file"}
	if target.Type == "image" {
		keys = []string{"sourcePath", "source_path", "filePath", "file_path", "localPath", "local_path", "path", "file", "url", "download_url", "file_url"}
	}
	if target.Type == "record" {
		keys = []string{"path", "file_path", "file", "url", "download_url", "file_url"}
	}
	for _, key := range keys {
		value := strings.TrimSpace(strings.TrimPrefix(stringFromAny(data[key]), "file://"))
		if normalizedHTTPURL(value) != "" {
			return value, "url"
		}
		if usableLocalMediaPath(value) && (!videoFileSegment(target) || localPathLooksLikeVideo(value)) {
			return value, "path"
		}
	}
	if target.Type == "image" || target.Type == "record" {
		if encoded := strings.TrimSpace(stringFromAny(data["base64"])); encoded != "" {
			return "base64://" + encoded, ""
		}
	}
	for _, key := range oneBotMediaContainerKeys() {
		switch value := data[key].(type) {
		case map[string]any:
			if source, sourceKey := mediaSourceFromOneBotData(value, target); source != "" {
				return source, sourceKey
			}
		case []any:
			for _, item := range value {
				segmentMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if source, sourceKey := mediaSourceFromOneBotData(segmentMap, target); source != "" {
					return source, sourceKey
				}
			}
		}
	}
	return "", ""
}

func usableOneBotMessageIDs(values ...string) []string {
	var out []string
	for _, value := range uniqueNonEmptyStrings(values...) {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil && id == 0 {
			continue
		}
		out = append(out, value)
	}
	return out
}

func mediaSegmentMatchesAny(segment MessageSegment, data map[string]any) bool {
	wantStable := segmentStableMediaIdentifiers(segment)
	gotStable := oneBotStableMediaIdentifiers(data)
	if len(wantStable) > 0 && len(gotStable) > 0 {
		return mediaIdentifiersIntersect(wantStable, gotStable)
	}
	want := segmentMediaIdentifiers(segment)
	if len(want) == 0 {
		return true
	}
	got := oneBotMediaIdentifiers(data)
	if len(got) == 0 {
		return true
	}
	return mediaIdentifiersIntersect(want, got)
}

func mediaIdentifiersIntersect(want, got []string) bool {
	for _, left := range want {
		for _, right := range got {
			if strings.EqualFold(left, right) || strings.EqualFold(filepath.Base(left), filepath.Base(right)) {
				return true
			}
		}
	}
	return false
}

func segmentStableMediaIdentifiers(segment MessageSegment) []string {
	values := make([]string, 0, 4)
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid"} {
		values = append(values, segment.Data[key])
	}
	return uniqueNonEmptyStrings(values...)
}

func oneBotStableMediaIdentifiers(data map[string]any) []string {
	values := make([]string, 0, 4)
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid"} {
		values = append(values, stringFromAny(data[key]))
	}
	return uniqueNonEmptyStrings(values...)
}

func imageFileToken(data map[string]string) string {
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid", "id", "file"} {
		if value := strings.TrimSpace(data[key]); value != "" {
			return value
		}
	}
	return ""
}

func imageFileTokenAny(data map[string]any) string {
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid", "id", "file"} {
		if value := strings.TrimSpace(stringFromAny(data[key])); value != "" {
			return value
		}
	}
	return ""
}

func stableImageFileTokenAny(data map[string]any) string {
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid"} {
		if value := strings.TrimSpace(stringFromAny(data[key])); value != "" {
			return value
		}
	}
	return ""
}

func segmentMediaIdentifiers(segment MessageSegment) []string {
	values := make([]string, 0, 10)
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid", "id", "file", "name", "filename", "fileName"} {
		values = append(values, segment.Data[key])
	}
	return uniqueNonEmptyStrings(values...)
}

func oneBotMediaIdentifiers(data map[string]any) []string {
	values := make([]string, 0, 10)
	for _, key := range []string{"file_id", "fileId", "file_uuid", "fileUuid", "id", "file", "name", "filename", "fileName"} {
		values = append(values, stringFromAny(data[key]))
	}
	return uniqueNonEmptyStrings(values...)
}

func oneBotMediaContainerKeys() []string {
	return []string{"data", "message", "messages", "segments", "element", "elements", "image", "picElement"}
}

func (r *Runtime) recoverOutgoingImageSegments(ctx context.Context, event MessageEvent) (MessageEvent, []error) {
	if r.channel == nil || strings.TrimSpace(event.MessageID) == "" {
		return event, []error{fmt.Errorf("sent image recovery has no OneBot message")}
	}
	response, err := r.callOneBotAPIForEvent(ctx, event, "get_msg", map[string]any{
		"message_id": oneBotMessageIDParam(event.MessageID),
	})
	if err != nil {
		return event, []error{fmt.Errorf("get sent message: %w", err)}
	}
	oneBotImages := oneBotImageSegmentData(response)
	if len(oneBotImages) == 0 {
		return event, []error{fmt.Errorf("sent OneBot message contains no image segments")}
	}

	out := append([]MessageSegment(nil), event.Segments...)
	var failures []error
	imageOrdinal := 0
	for index, segment := range out {
		if segment.Type != "image" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
			imageOrdinal++
			continue
		}
		if imageOrdinal >= len(oneBotImages) {
			failures = append(failures, fmt.Errorf("sent image %d is missing from OneBot message", imageOrdinal+1))
			imageOrdinal++
			continue
		}

		oneBotImage := oneBotImages[imageOrdinal]
		imageOrdinal++
		data := cloneSegmentData(segment.Data)
		token := imageFileTokenAny(oneBotImage)
		if stableToken := stableImageFileTokenAny(oneBotImage); stableToken != "" {
			data["file_id"] = stableToken
		}
		source, sourceKey := mediaSourceFromOneBotData(oneBotImage, MessageSegment{Type: "image"})
		if token != "" {
			resolved, resolveErr := r.callOneBotAPIForEvent(ctx, event, "get_image", map[string]any{"file": token})
			if resolveErr == nil {
				if resolvedSource, resolvedKey := mediaSourceFromOneBotData(resolved, MessageSegment{Type: "image"}); resolvedSource != "" {
					source, sourceKey = resolvedSource, resolvedKey
				}
			}
		}
		if source == "" {
			failures = append(failures, fmt.Errorf("sent image %d has no readable OneBot source", imageOrdinal))
			continue
		}
		appendResolvedImageSource(data, source, sourceKey)
		out[index].Data = data
	}
	event.Segments = out
	return event, failures
}

func oneBotImageSegmentData(value any) []map[string]any {
	var out []map[string]any
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, entry := range item {
				visit(entry)
			}
		case []map[string]any:
			for _, entry := range item {
				visit(entry)
			}
		case map[string]any:
			if strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "image") {
				if data, ok := item["data"].(map[string]any); ok {
					out = append(out, data)
				}
				return
			}
			for _, key := range []string{"data", "message", "messages", "segments"} {
				visit(item[key])
			}
		}
	}
	visit(value)
	return out
}

func segmentHasMediaSource(segment MessageSegment) bool {
	keys := []string{"cached_file", "url", "download_url", "file_url", "video_url", "src", "sourcePath", "source_path", "filePath", "file_path", "localPath", "local_path", "path", "file"}
	if segment.Type == "image" {
		for index := 1; index <= 8; index++ {
			keys = append(keys, fmt.Sprintf("%s%d", imageResolvedSourceKey, index))
		}
	}
	for _, key := range keys {
		value := strings.TrimSpace(segment.Data[key])
		if normalizedHTTPURL(value) != "" {
			return true
		}
		if usableLocalMediaPath(value) && (!videoFileSegment(segment) || localPathLooksLikeVideo(value)) {
			return true
		}
		if !videoFileSegment(segment) && (strings.HasPrefix(value, "data:image/") || strings.HasPrefix(value, "base64://")) {
			return true
		}
	}
	return false
}

func usableLocalMediaPath(value string) bool {
	path := rawAbsoluteMediaPath(value)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func videoFileSegment(segment MessageSegment) bool {
	if segment.Type == "video" {
		return true
	}
	if segment.Type != "file" {
		return false
	}
	_, ok := videoMediaExtensions[strings.ToLower(filepath.Ext(mediaSegmentName(segment)))]
	return ok
}

func mediaSegmentName(segment MessageSegment) string {
	return firstNonEmpty(segment.Data["name"], segment.Data["filename"], segment.Data["fileName"], segment.Data["file"])
}

func videoSourceCandidates(segments []MessageSegment) []string {
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, segment := range segments {
		if !videoFileSegment(segment) {
			continue
		}
		for _, key := range []string{"url", "download_url", "file_url", "video_url", "src", "path", "file_path", "file"} {
			value := strings.TrimSpace(segment.Data[key])
			if value == "" {
				continue
			}
			if normalizedHTTPURL(value) == "" {
				if rawAbsoluteMediaPath(value) == "" || !localPathLooksLikeVideo(value) {
					continue
				}
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func localPathLooksLikeVideo(value string) bool {
	path := rawAbsoluteMediaPath(value)
	if path == "" {
		return false
	}
	_, ok := videoMediaExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func mergeQuotedMessageMedia(live, stored *QuotedMessage) *QuotedMessage {
	if live == nil {
		return stored
	}
	if stored == nil {
		return live
	}
	merged := *live
	merged.Segments = mergePersistedMediaSegments(live.Segments, stored.Segments)
	if strings.TrimSpace(merged.SemanticSourceMessageID) == "" {
		merged.SemanticSourceMessageID = strings.TrimSpace(stored.SemanticSourceMessageID)
	}
	merged.SemanticSourceMessageIDs = semanticSourceMessageIDs(
		merged.SemanticSourceMessageID,
		append(append([]string(nil), merged.SemanticSourceMessageIDs...), quotedSemanticSourceMessageIDs(stored)...),
	)
	if len(merged.SemanticSourceMessageIDs) > 0 {
		merged.SemanticSourceMessageID = merged.SemanticSourceMessageIDs[0]
	}
	return &merged
}

func mergePersistedMediaSegments(live, stored []MessageSegment) []MessageSegment {
	out := append([]MessageSegment(nil), live...)
	for _, persisted := range stored {
		if persisted.Type != "image" && persisted.Type != "video" && persisted.Type != "file" {
			continue
		}
		if persisted.Type == "image" && strings.TrimSpace(persisted.Data["cached_file"]) == "" {
			continue
		}
		match := -1
		for index := range out {
			if mediaSegmentsMatch(out[index], persisted) {
				match = index
				break
			}
		}
		if match < 0 {
			out = append(out, persisted)
			continue
		}
		data := cloneSegmentData(out[match].Data)
		for key, value := range persisted.Data {
			if strings.TrimSpace(data[key]) == "" || strings.HasPrefix(key, "cached_") || key == "source_type" || key == "frame_index" {
				data[key] = value
			}
		}
		out[match].Data = data
	}
	return out
}

func mediaSegmentsMatch(left, right MessageSegment) bool {
	if left.Type != right.Type {
		return false
	}
	if left.Type == "image" && (left.Data["source_type"] == "video_frame" || right.Data["source_type"] == "video_frame") {
		return left.Data["source_type"] == right.Data["source_type"] &&
			left.Data["frame_index"] == right.Data["frame_index"] &&
			left.Data["video_index"] == right.Data["video_index"] &&
			left.Data["source_message_id"] == right.Data["source_message_id"]
	}
	for _, key := range []string{"file_id", "id", "fid", "file", "name", "filename", "url"} {
		leftValue := strings.TrimSpace(left.Data[key])
		rightValue := strings.TrimSpace(right.Data[key])
		if leftValue != "" && rightValue != "" && strings.EqualFold(filepath.Base(leftValue), filepath.Base(rightValue)) {
			return true
		}
	}
	return false
}

func cloneSegmentData(data map[string]string) map[string]string {
	out := make(map[string]string, len(data)+2)
	for key, value := range data {
		out[key] = value
	}
	return out
}
