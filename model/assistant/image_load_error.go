package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/applog"
)

var errImageMediaUnavailable = errors.New("assistant: image media unavailable")

type imageMediaUnavailableError struct {
	count int
	cause error
}

func (e *imageMediaUnavailableError) Error() string {
	if e == nil {
		return errImageMediaUnavailable.Error()
	}
	if e.cause == nil {
		return fmt.Sprintf("%s: %d image(s)", errImageMediaUnavailable, e.count)
	}
	return fmt.Sprintf("%s: %d image(s): %v", errImageMediaUnavailable, e.count, e.cause)
}

func (e *imageMediaUnavailableError) Unwrap() error {
	return errImageMediaUnavailable
}

func newImageMediaUnavailableError(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	return &imageMediaUnavailableError{count: len(failures), cause: errors.Join(failures...)}
}

func newImageMediaUnavailableErrorWithDiagnostics(failures, diagnostics []error) error {
	if len(failures) == 0 {
		return nil
	}
	if len(diagnostics) == 0 {
		diagnostics = failures
	}
	return &imageMediaUnavailableError{count: len(failures), cause: errors.Join(diagnostics...)}
}

func (r *Runtime) prepareEventImages(ctx context.Context, event MessageEvent) MessageEvent {
	event.imageResolutionRun = true
	event.imageLoadErr = nil

	var diagnostics []error
	var resolveFailures []error
	event, resolveFailures = r.enrichMediaReferencesDetailed(ctx, event)
	diagnostics = append(diagnostics, resolveFailures...)

	var loadFailures []error
	event, loadFailures = cacheMessageEventImagesDetailed(ctx, event)
	if len(loadFailures) == 0 {
		return event
	}
	diagnostics = append(diagnostics, loadFailures...)

	// A syntactically valid QQ CDN URL is not proof that its rkey still works.
	// First validate get_image directly; only if that source also fails do we ask
	// get_msg to refresh NapCat's message-media map and retry get_image.
	for range 2 {
		event, resolveFailures = r.enrichMediaReferencesDetailed(ctx, event)
		diagnostics = append(diagnostics, resolveFailures...)
		event, loadFailures = cacheMessageEventImagesDetailed(ctx, event)
		if len(loadFailures) == 0 {
			return event
		}
		diagnostics = append(diagnostics, loadFailures...)
	}
	event.imageLoadErr = newImageMediaUnavailableErrorWithDiagnostics(loadFailures, diagnostics)
	r.recordImageLoadError(ctx, event, event.imageLoadErr)
	return event
}

func (r *Runtime) recordImageLoadError(ctx context.Context, event MessageEvent, err error) {
	writer := r.appLogWriter()
	if writer == nil || err == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "qqbot.image.load",
		Message: "图片读取失败",
		Detail:  err.Error(),
		Actor:   qqEventActor(event),
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"group_id":   event.GroupID,
			"user_id":    event.UserID,
			"message_id": event.MessageID,
		},
	})
}
