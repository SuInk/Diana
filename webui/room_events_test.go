// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"github.com/SuInk/diana/model/assistant"
	"testing"
)

func TestRoomChangesInvalidateSSEStatusSignature(t *testing.T) {
	status := assistant.RuntimeStatus{Rooms: []assistant.RoomState{{ProfileID: "one", Epoch: "boot", Revision: 1, Pose: "rest"}}}
	before := statusSignature(status)
	status.Rooms[0].Revision++
	status.Rooms[0].Pose = "phone"
	if statusSignature(status) == before {
		t.Fatal("room changes would not reach SSE clients")
	}
	before = statusSignature(status)
	status.Rooms[0].Epoch = "restart"
	if statusSignature(status) == before {
		t.Fatal("restart did not change signature")
	}
}
