// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) RecordInboundEventReplyMerge(ctx context.Context, event assistant.MessageEvent, rootTurnID string) error {
	return s.MarkInboundHandoff(ctx, assistant.InboundHandoffRef{Event: event}, rootTurnID)
}
