// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"github.com/SuInk/diana/model/agent"
	"testing"
)

func TestReplyIntentIgnoresLegacyEvidenceField(t *testing.T) {
	raw := `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":true,"needs_evidence":true}`
	_, scope, ok := parseReplyIntentDecision(raw, agent.NewToolRegistry())
	if !ok || !scope.Routed || !scope.KeepContextSummary {
		t.Fatalf("legacy field affected routing: ok=%v scope=%+v", ok, scope)
	}
}
