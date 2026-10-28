// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSlangCandidateObserveAccumulates(t *testing.T) {
	store := newNotebookStore(t)
	ctx := context.Background()
	now := time.Now()
	observe := func(speaker, text string) assistant.SlangCandidate {
		t.Helper()
		candidate, err := store.ObserveSlangCandidate(ctx, assistant.SlangObservation{
			ProfileID: "bot-a", GroupID: "10001", Term: "开盒", SpeakerID: speaker,
			Evidence: assistant.SlangEvidence{Sender: speaker, Text: text}, Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	observe("u1", "第一句")
	observe("u1", "第二句")
	observe("u2", "第三句")
	got := observe("u2", "第四句")
	if got.Count != 4 || len(got.Speakers) != 2 || got.Status != assistant.SlangCandidatePending || got.NextThreshold != 3 {
		t.Fatalf("candidate = %+v", got)
	}
	if len(got.Evidence) != 3 || got.Evidence[0].Text != "第二句" || got.Evidence[2].Text != "第四句" {
		t.Fatalf("evidence should keep the latest three: %+v", got.Evidence)
	}
	// 别的群、别的机器人各算各的。
	other, err := store.ObserveSlangCandidate(ctx, assistant.SlangObservation{ProfileID: "bot-a", GroupID: "10002", Term: "开盒", SpeakerID: "u1", Now: now})
	if err != nil || other.Count != 1 {
		t.Fatalf("other group = %+v err=%v", other, err)
	}
}

func TestSlangCandidateUpdateAndResearchCount(t *testing.T) {
	store := newNotebookStore(t)
	ctx := context.Background()
	now := time.Now()
	for index := 0; index < 3; index++ {
		term := fmt.Sprintf("词%d", index)
		if _, err := store.ObserveSlangCandidate(ctx, assistant.SlangObservation{ProfileID: "bot-a", GroupID: "10001", Term: term, SpeakerID: "u1", Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateSlangCandidate(ctx, "bot-a", "10001", "词0", assistant.SlangCandidatePending, 3, true, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSlangCandidate(ctx, "bot-a", "10001", "词1", assistant.SlangCandidatePending, 3, true, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSlangCandidate(ctx, "bot-a", "10001", "词2", assistant.SlangCandidateUnresolved, 96, false, now); err != nil {
		t.Fatal(err)
	}
	count, err := store.CountSlangResearch(ctx, "bot-a", now.Add(-time.Hour))
	if err != nil || count != 1 {
		t.Fatalf("research count = %d err=%v, want 1", count, err)
	}
	got, err := store.ObserveSlangCandidate(ctx, assistant.SlangObservation{ProfileID: "bot-a", GroupID: "10001", Term: "词2", SpeakerID: "u2", Now: now})
	if err != nil || got.Status != assistant.SlangCandidateUnresolved || got.NextThreshold != 96 || got.Count != 2 {
		t.Fatalf("observing must keep status and threshold: %+v err=%v", got, err)
	}
}
