package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestScopedModelRolePersistencePreservesActiveAndOtherConfig(t *testing.T) {
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
	a := assistant.BotConfig{ID: "a", OwnerID: "11"}
	b := assistant.BotConfig{ID: "b", OwnerID: "22", SystemPrompt: "keep this", ModelRoles: map[string]assistant.ModelRole{"vision": {ProfileID: "one", Model: "vision"}}}
	if err := store.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{a, b}}); err != nil {
		t.Fatal(err)
	}
	persist := NewRuntimePersistor(store)
	if _, err := persist.SaveModelRole(b, "chat", assistant.ModelRole{ProfileID: "two", Model: "chat"}); err != nil {
		t.Fatal(err)
	}
	saved, _, err := db.LoadBotProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveID != "a" || saved.Profiles[1].SystemPrompt != "keep this" || saved.Profiles[1].ModelRoles["vision"].Model != "vision" || saved.Profiles[1].ModelRoles["chat"].ProfileID != "two" {
		t.Fatal("scoped save changed unrelated configuration")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := persist.SaveModelRole(b, "chat", assistant.ModelRole{ProfileID: "one", Model: "wrong"}); err == nil {
		t.Fatal("persistence failure hidden")
	}
	if store.Profiles().Profiles[1].ModelRoles["chat"].ProfileID != "two" {
		t.Fatal("failed persistence changed in-memory store")
	}
}

func TestModelRoleStoreMergesLatestConfigurationAndRechecksOwner(t *testing.T) {
	expected := assistant.BotConfig{ID: "bot", OwnerID: "11", SystemPrompt: "old persona"}
	latest := expected
	latest.SystemPrompt = "new persona"
	latest.ModelRoles = map[string]assistant.ModelRole{"vision": {ProfileID: "vision", Model: "vision-model"}}
	set := assistant.ProfileSet{ActiveID: "bot", Profiles: []assistant.BotConfig{latest}}
	_, saved, err := updateStoredModelRole(set, expected, "chat", assistant.ModelRole{ProfileID: "two", Model: "new"})
	if err != nil || saved.SystemPrompt != "new persona" || saved.ModelRoles["vision"].Model != "vision-model" {
		t.Fatalf("latest settings overwritten: %+v %v", saved, err)
	}
	set.Profiles[0].OwnerID = "22"
	if _, _, err := updateStoredModelRole(set, expected, "chat", assistant.ModelRole{ProfileID: "two", Model: "new"}); err == nil {
		t.Fatal("revoked owner request committed")
	}
}
