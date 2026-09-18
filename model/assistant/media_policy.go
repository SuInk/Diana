package assistant

import "context"

// This context is confined to passive ingestion. Explicit reads keep using the
// normal media path, even when background preprocessing is disabled.
type skipAutomaticVideoKey struct{}

func (r *Runtime) withAutomaticMediaPolicy(ctx context.Context, event MessageEvent) context.Context {
	return context.WithValue(ctx, skipAutomaticVideoKey{}, !boolValue(r.effectiveConfigForEvent(event).AutoVideoPreprocess, true))
}

func skipAutomaticVideo(ctx context.Context) bool {
	disabled, _ := ctx.Value(skipAutomaticVideoKey{}).(bool)
	return disabled
}

func isMediaParsePurpose(purpose string) bool {
	switch purpose {
	case PurposeMediaParse, "image_description_cache", "sticker_description", "image_describe", "image_ocr":
		return true
	default:
		return false
	}
}

// prepareRequestedVideoFrames materializes only video references selected by a
// history-media tool call. Existing frames are reused on subsequent reads.
func (r *Runtime) prepareRequestedVideoFrames(ctx context.Context, event MessageEvent) MessageEvent {
	ctx = context.WithValue(r.withFileParserVideoLimit(ctx, event), skipAutomaticVideoKey{}, false)
	prepare := func(source MessageEvent) []MessageSegment {
		if hasCachedVideoFrames(source.Segments) {
			return source.Segments
		}
		segments := append([]MessageSegment(nil), source.Segments...)
		for i, segment := range segments {
			if !videoFileSegment(segment) {
				continue
			}
			resolved, _ := r.enrichMediaSegmentsDetailed(ctx, source, []MessageSegment{segment})
			if len(resolved) == 1 {
				segments[i] = resolved[0]
			}
		}
		return cacheVideoFrames(ctx, source.Platform, source.Time, string(source.Kind), source.GroupID, source.UserID, source.MessageID, segments)
	}
	event.Segments = prepare(event)
	if event.Quoted != nil {
		quoted := *event.Quoted
		source := event
		source.GroupID = firstNonEmpty(quoted.GroupID, event.GroupID)
		source.UserID = firstNonEmpty(quoted.UserID, event.UserID)
		source.MessageID = quoted.MessageID
		source.Segments = quoted.Segments
		source.Quoted = nil
		quoted.Segments = prepare(source)
		event.Quoted = &quoted
	}
	return event
}
