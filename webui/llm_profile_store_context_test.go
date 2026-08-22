// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"
)

func legacyContextProfileSet() llm.ProfileSet {
	return llm.ProfileSet{
		ActiveID: "legacy",
		Profiles: []llm.Profile{{
			ID:    "legacy",
			Name:  "旧配置",
			Group: llm.GroupChat,
			Config: llm.ProviderConfig{
				Provider:            llm.ProviderOpenAICompatible,
				Model:               "claude-sonnet-4",
				BaseURL:             "https://api.example.test/v1",
				ContextWindowTokens: llm.LegacyDefaultContextWindowTokens,
				MaxContextTokens:    llm.LegacyDefaultContextWindowTokens,
			},
		}},
	}
}

func TestPersistentLLMProfileStoreClearsLegacyContextFallback(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "llm-context.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.SaveLLMProfiles(ctx, legacyContextProfileSet()); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}

	profileStore, err := NewPersistentLLMProfileStore(ctx, store, llm.ProviderConfig{})
	if err != nil {
		t.Fatalf("open profile store: %v", err)
	}
	if got := profileStore.Current().MaxContextTokensWithDefault(); got != 200000 {
		t.Fatalf("context budget after migration = %d", got)
	}

	saved, ok, err := store.LoadLLMProfiles(ctx)
	if err != nil || !ok {
		t.Fatalf("reload profiles: ok=%v err=%v", ok, err)
	}
	if saved.Profiles[0].Config.ContextWindowTokens != 0 {
		t.Fatalf("persisted window = %d, want cleared", saved.Profiles[0].Config.ContextWindowTokens)
	}
}

func TestPersistentLLMProfileStoreRunsLegacyMigrationOnce(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "llm-context-once.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.SaveLLMProfiles(ctx, legacyContextProfileSet()); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if _, err := NewPersistentLLMProfileStore(ctx, store, llm.ProviderConfig{}); err != nil {
		t.Fatalf("first open: %v", err)
	}

	// 用户后来自己把窗口填回 16384：第二次启动不能再当成历史兜底值清掉。
	deliberate := legacyContextProfileSet()
	if err := store.SaveLLMProfiles(ctx, deliberate); err != nil {
		t.Fatalf("save deliberate profiles: %v", err)
	}
	profileStore, err := NewPersistentLLMProfileStore(ctx, store, llm.ProviderConfig{})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if got := profileStore.Current().MaxContextTokensWithDefault(); got != llm.LegacyDefaultContextWindowTokens {
		t.Fatalf("deliberate 16K budget = %d, want preserved", got)
	}
}

func TestPersistentLLMProfileStoreDoesNotPersistDerivedWindow(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "llm-context-save.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	profileStore, err := NewPersistentLLMProfileStore(ctx, store, llm.ProviderConfig{})
	if err != nil {
		t.Fatalf("open profile store: %v", err)
	}

	set := llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		Model:    "gemini-2.5-pro",
		BaseURL:  "https://api.example.test/v1",
	})
	profileStore.SaveProfiles(set)

	saved, ok, err := store.LoadLLMProfiles(ctx)
	if err != nil || !ok {
		t.Fatalf("reload profiles: ok=%v err=%v", ok, err)
	}
	if saved.Profiles[0].Config.ContextWindowTokens != 0 || saved.Profiles[0].Config.MaxContextTokens != 0 {
		t.Fatalf("persisted budgets = %d/%d, want derived values stripped",
			saved.Profiles[0].Config.ContextWindowTokens, saved.Profiles[0].Config.MaxContextTokens)
	}
	if got := profileStore.Current().MaxContextTokensWithDefault(); got != 1000000 {
		t.Fatalf("effective budget = %d", got)
	}
}
