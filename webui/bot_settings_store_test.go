package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestParticipationStorePersistsOneRobotAndPropagatesFailure(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a := assistant.BotConfig{ID: "a", OwnerID: "one", SystemPrompt: "unchanged"}
	b := assistant.BotConfig{ID: "b", OwnerID: "two", WelcomeMessage: "原欢迎词", WelcomeMode: assistant.WelcomeModeLLM, WelcomeLLMCooldownSeconds: 30}
	if err := store.SaveProfiles(assistant.ProfileSet{Profiles: []assistant.BotConfig{a, b}}); err != nil {
		t.Fatal(err)
	}
	p := NewRuntimePersistor(store)
	enabled := true
	if _, err := p.SaveBotSettings(b, assistant.BotSettingsUpdate{Participation: &assistant.ParticipationPreferences{Desire: 0, CooldownSeconds: 120}, WelcomeEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	set, _, err := db.LoadBotProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Profiles[0].SystemPrompt != "unchanged" || set.Profiles[0].Participation != nil || set.Profiles[1].Participation.Desire != 0 || set.Profiles[1].Participation.CooldownSeconds != 120 {
		t.Fatalf("incorrect stored config: %+v", set)
	}
	if set.Profiles[0].WelcomeEnabled || !set.Profiles[1].WelcomeEnabled || set.Profiles[1].WelcomeMessage != "原欢迎词" || set.Profiles[1].WelcomeMode != assistant.WelcomeModeLLM || set.Profiles[1].WelcomeLLMCooldownSeconds != 30 {
		t.Fatalf("mixed settings update lost welcome fields or affected another bot: %+v", set)
	}
	// The expected profile is deliberately stale. A welcome-only patch must keep
	// the participation settings from the latest stored profile.
	message := "新的欢迎词"
	if _, err := p.SaveBotSettings(b, assistant.BotSettingsUpdate{WelcomeMessage: &message}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restored := reloaded.Profiles().Profiles[1]
	if restored.WelcomeMessage != message || !restored.WelcomeEnabled || restored.Participation.Desire != 0 || restored.Participation.CooldownSeconds != 120 {
		t.Fatalf("welcome-only patch or restart lost settings: %+v", restored)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := p.SaveBotSettings(b, assistant.BotSettingsUpdate{Participation: &assistant.ParticipationPreferences{Desire: 100}, WelcomeEnabled: &disabled}); err == nil {
		t.Fatal("storage failure hidden")
	}
	if store.Profiles().Profiles[1].Participation.Desire != 0 || !store.Profiles().Profiles[1].WelcomeEnabled {
		t.Fatal("failed storage changed active values")
	}
}

func TestBotSettingsStoreRejectsStaleOwner(t *testing.T) {
	store := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{{ID: "b", OwnerID: "new-owner"}}})
	enabled := true
	if _, err := store.SaveBotSettings(assistant.BotConfig{ID: "b", OwnerID: "old-owner"}, assistant.BotSettingsUpdate{WelcomeEnabled: &enabled}); err == nil {
		t.Fatal("stale owner changed welcome settings")
	}
	if store.Profiles().Profiles[0].WelcomeEnabled {
		t.Fatal("rejected stale-owner update mutated settings")
	}
}
