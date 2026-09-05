// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type sharedPublicRecallStore struct {
	testStructuredMemoryStore
	shared    []StructuredMemoryItem
	sharedErr error
	requests  []StructuredMemoryQuery
}

func (s *sharedPublicRecallStore) ListStructuredMemories(_ context.Context, query StructuredMemoryQuery) ([]StructuredMemoryItem, error) {
	s.requests = append(s.requests, query)
	if query.SharedPublicOnly {
		return s.shared, s.sharedErr
	}
	return s.items, nil
}

func TestSharedPublicRecallHasReservedCandidatesWithoutMembership(t *testing.T) {
	for _, platformSharing := range []bool{false, true} {
		t.Run(fmt.Sprint(platformSharing), func(t *testing.T) {
			cfg := DefaultBotConfig()
			cfg.ID, cfg.Platform, cfg.Enabled = "qq", PlatformOneBotV11, true
			cfg.CrossGroupMemoryEnabled = boolPointer(!platformSharing)
			cfg.CrossPlatformMemoryEnabled = boolPointer(platformSharing)
			channel := &crossGroupMembershipChannel{}
			r := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
			source := cfg
			source.ID, source.Platform = "tg", PlatformTelegram
			r.SetProfiles(ProfileSet{ActiveID: "qq", Profiles: []BotConfig{cfg, source}})
			store := &sharedPublicRecallStore{}
			for i := 0; i < structuredMemoryLoadLimit; i++ {
				store.items = append(store.items, StructuredMemoryItem{ID: fmt.Sprint(i), Kind: MemoryKindFact, Topic: "旧话题", Content: "本群的其他讨论", Confidence: 0.99, Importance: 0.99})
			}
			sourceSession, label := "qq:group:other", "其他群公共记忆"
			if platformSharing {
				sourceSession, label = "tg:group:other", "其他平台群公共记忆"
			}
			item := StructuredMemoryItem{ID: "shared", Kind: MemoryKindSummary, Topic: "Aurora 发布计划", Content: "Aurora 发布计划已确定周五上线", SourceSession: sourceSession, Visibility: MemoryVisibilitySession, Confidence: 0.99, Importance: 0.9, LastVerifiedAt: time.Now()}
			store.shared = []StructuredMemoryItem{item, item}
			r.SetStructuredMemoryStore(store)
			event := MessageEvent{Kind: EventKindGroup, Platform: cfg.Platform, ProfileID: "qq", ContextNamespace: "qq", GroupID: "target", UserID: "user", RawMessage: "Aurora 发布计划"}
			text := r.memoryContext(context.Background(), event, event.RawMessage)
			if !strings.Contains(text, "周五上线") || !strings.Contains(text, label) || strings.Count(text, "周五上线") != 1 {
				t.Fatalf("shared memory missing, duplicated or misattributed: %s", text)
			}
			if len(store.requests) != 2 || !store.requests[1].SharedPublicOnly || store.requests[1].MaxCandidates != sharedPublicMemoryLoadLimit {
				t.Fatalf("no reserved retrieval: %+v", store.requests)
			}
			if len(channel.callsSnapshot()) != 0 {
				t.Fatal("public memory required membership checks")
			}
			store.requests = nil
			cfg.CrossGroupMemoryEnabled, cfg.CrossPlatformMemoryEnabled = boolPointer(false), boolPointer(false)
			r.SetProfiles(ProfileSet{ActiveID: "qq", Profiles: []BotConfig{cfg, source}})
			text = r.memoryContext(context.Background(), event, event.RawMessage)
			if len(store.requests) != 1 || strings.Contains(text, "周五上线") {
				t.Fatal("sharing continued after disabling the switch")
			}
		})
	}
}

func TestSharedPublicRecallFailurePreservesLocalMemory(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.CrossGroupMemoryEnabled = boolPointer(true)
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &sharedPublicRecallStore{sharedErr: errors.New("query timeout")}
	store.items = []StructuredMemoryItem{{ID: "local", Kind: MemoryKindFact, Topic: "Aurora", Content: "Aurora 本群约定周六发布", SourceSession: "group:target", Confidence: 0.99, Importance: 0.9}}
	r.SetStructuredMemoryStore(store)
	text := r.memoryContext(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "target", UserID: "user"}, "Aurora")
	if !strings.Contains(text, "周六发布") {
		t.Fatalf("shared query failure dropped local memory: %s", text)
	}
}
