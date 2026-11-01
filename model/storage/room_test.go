// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"github.com/SuInk/diana/model/assistant"
	"path/filepath"
	"testing"
	"time"
)

func TestRoomIntentIsolationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	intent := assistant.RoomIntent{Pose: "desk", UpdatedAt: time.Now()}
	if err := s.SaveRoomIntent(ctx, "one", intent); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRoomIntent(ctx, "", intent); err == nil {
		t.Fatal("empty profile accepted")
	}
	if err := s.SaveRoomIntent(ctx, "one", assistant.RoomIntent{Pose: "pay", UpdatedAt: time.Now()}); err == nil {
		t.Fatal("invalid pose accepted")
	}
	_ = s.Close()
	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadRoomIntent(ctx, "one")
	if err != nil || got.Pose != "desk" || !got.UpdatedAt.Equal(intent.UpdatedAt) {
		t.Fatalf("restart: %+v %v", got, err)
	}
	other, err := s.LoadRoomIntent(ctx, "two")
	if err != nil || other.Pose != "rest" || !other.UpdatedAt.IsZero() {
		t.Fatalf("isolation: %+v %v", other, err)
	}
}
