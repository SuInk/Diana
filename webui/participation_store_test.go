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
	b := assistant.BotConfig{ID: "b", OwnerID: "two"}
	if err := store.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{a, b}}); err != nil {
		t.Fatal(err)
	}
	p := NewRuntimePersistor(store)
	if _, err := p.SaveParticipation(b, assistant.ParticipationPreferences{Desire: 0, CooldownSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	set, _, err := db.LoadBotProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.ActiveID != "a" || set.Profiles[0].SystemPrompt != "unchanged" || set.Profiles[0].Participation != nil || set.Profiles[1].Participation.Desire != 0 || set.Profiles[1].Participation.CooldownSeconds != 120 {
		t.Fatalf("incorrect stored config: %+v", set)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SaveParticipation(b, assistant.ParticipationPreferences{Desire: 100}); err == nil {
		t.Fatal("storage failure hidden")
	}
	if store.Profiles().Profiles[1].Participation.Desire != 0 {
		t.Fatal("failed storage changed active values")
	}
}
