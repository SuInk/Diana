package assistant

import (
	"context"
	"sync"
	"time"
)

type outboundMediaContextKey struct{}

func outboundMessageContext(ctx context.Context, msg OutgoingMessage) context.Context {
	media := len(msg.ImageURLs)+len(msg.VideoURLs)+len(msg.AudioURLs) > 0
	for _, segment := range msg.Segments {
		switch segment.Type {
		case "image", "record", "video", "file", "forward":
			media = true
		}
	}
	return context.WithValue(ctx, outboundMediaContextKey{}, media)
}

func lockOutboundDelivery(ctx context.Context, lock *sync.Mutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lock.TryLock() {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
