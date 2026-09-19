// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func TestSendOneBotInputStatusPrivateOnly(t *testing.T) {
	var calls []map[string]any
	var actions []string
	call := func(_ context.Context, action string, params map[string]any) (map[string]any, error) {
		actions = append(actions, action)
		calls = append(calls, params)
		return nil, nil
	}
	if err := sendOneBotInputStatus(context.Background(), OutgoingMessage{GroupID: "100", UserID: "42"}, "typing", call); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("group chat should not call set_input_status: %v", actions)
	}
	if err := sendOneBotInputStatus(context.Background(), OutgoingMessage{UserID: "42"}, "typing", call); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || actions[0] != "set_input_status" || calls[0]["user_id"] != int64(42) || calls[0]["event_type"] != 1 {
		t.Fatalf("unexpected call: %v %v", actions, calls)
	}
}

func TestQQTypingEnabledDefaultsOn(t *testing.T) {
	if !boolValue(BotConfig{}.WithDefaults().QQTypingEnabled, false) {
		t.Fatal("qq typing should default to enabled")
	}
	off := false
	if boolValue(BotConfig{QQTypingEnabled: &off}.WithDefaults().QQTypingEnabled, true) {
		t.Fatal("explicit false must survive defaults")
	}
}
