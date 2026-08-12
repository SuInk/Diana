package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	out := append([]MessageSegment(nil), segments...)
	var failures []error
	for index, segment := range out {
		if segment.Type != "image" && !videoFileSegment(segment) {
			continue
		}
		if segment.Type == "image" {
			needsFallback := strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true")
			if !needsFallback && segmentHasMediaSource(segment) {
				continue
			}
		} else if segmentHasMediaSource(segment) {
			continue
		}
		data := cloneSegmentData(segment.Data)
		sourceGroupID := firstNonEmpty(data["source_group_id"], event.GroupID)
		sourceMessageIDs := uniqueNonEmptyStrings(data["source_message_id"], event.MessageID)
		var requests []oneBotFileResolveRequest
		if segment.Type == "image" {
			file := firstNonEmpty(data["file"], data["file_id"], data["id"])
			if file != "" && resolvedImageSourceCount(data) == 0 {
				requests = append(requests, oneBotFileResolveRequest{action: "get_image", params: map[string]any{"file": file}})
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
					token := firstNonEmpty(mediaFileTokenFromOneBotData(response, segment), data["file"], data["file_id"], data["id"])
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
			resolved, resolveErr := r.callOneBotAPIForEvent(callCtx, event, "get_file", map[string]any{"file": token})
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
	for _, key := range []string{"data", "message", "messages", "segments"} {
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
		if segmentType == "image" || segmentType == "video" || segmentType == "file" {
			if segmentData, ok := item["data"].(map[string]any); ok && mediaSegmentMatchesAny(target, segmentData) {
				return firstNonEmpty(
					stringFromAny(segmentData["file"]),
					stringFromAny(segmentData["file_id"]),
				)
			}
		}
		for _, key := range []string{"data", "message", "messages", "segments"} {
			if token := mediaFileTokenFromOneBotValue(item[key], target); token != "" {
				return token
			}
		}
	}
	return ""
}

func mediaSourceFromOneBotData(data map[string]any, target MessageSegment) (string, string) {
	keys := []string{"url", "download_url", "file_url", "video_url", "path", "file_path", "file"}
	if target.Type == "image" {
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
	for _, key := range []string{"data", "message", "messages", "segments"} {
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
				if dataMap, ok := segmentMap["data"].(map[string]any); ok && mediaSegmentMatchesAny(target, dataMap) {
					if source, sourceKey := mediaSourceFromOneBotData(dataMap, target); source != "" {
						return source, sourceKey
					}
				}
				if source, sourceKey := mediaSourceFromOneBotData(segmentMap, target); source != "" {
					return source, sourceKey
				}
			}
		}
	}
	return "", ""
}

func mediaSegmentMatchesAny(segment MessageSegment, data map[string]any) bool {
	want := strings.TrimSpace(filepath.Base(mediaSegmentName(segment)))
	if want == "" || want == "." {
		return true
	}
	got := strings.TrimSpace(filepath.Base(firstNonEmpty(
		stringFromAny(data["name"]), stringFromAny(data["filename"]), stringFromAny(data["file"]),
	)))
	return got == "" || strings.EqualFold(got, want)
}

func segmentHasMediaSource(segment MessageSegment) bool {
	keys := []string{"cached_file", "url", "download_url", "file_url", "video_url", "src", "path", "file_path", "file"}
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
	return firstNonEmpty(segment.Data["name"], segment.Data["filename"], segment.Data["file"])
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
		return left.Data["source_type"] == right.Data["source_type"] && left.Data["frame_index"] == right.Data["frame_index"]
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
