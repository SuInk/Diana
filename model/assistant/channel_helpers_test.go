// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"
)

func TestEventDeduperRejectsRepeats(t *testing.T) {
	deduper := newEventDeduper(time.Minute)
	if !deduper.Accept("evt-1") {
		t.Fatal("first delivery was rejected")
	}
	if deduper.Accept("evt-1") {
		t.Fatal("repeated delivery of the same event was accepted")
	}
	if !deduper.Accept("evt-2") {
		t.Fatal("a different event was rejected")
	}
	// 没有事件 ID 时无法去重，只能放行，不然会静默丢消息。
	if !deduper.Accept("") {
		t.Fatal("an event without an id should be allowed through")
	}
}
