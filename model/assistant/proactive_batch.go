// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"time"
)

const ()

var errChatInReplyDeclined = errors.New("diana: chat-in generation declined to produce a substantive reply")

type proactiveReplyCandidate struct {
	Event      MessageEvent
	Text       string
	QueuedAt   time.Time
	Generation uint64
}
