package storage

import (
	"context"
	"testing"
)

func TestLoadBrowserBoxMigratesMissingHeadfulToVisible(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO app_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, browserBoxKey, `{"settings":{"enabled":true}}`); err != nil {
		t.Fatal(err)
	}
	doc, ok, err := store.LoadBrowserBox(context.Background())
	if err != nil || !ok {
		t.Fatalf("LoadBrowserBox() = %#v, %v, %v", doc, ok, err)
	}
	if !doc.Settings.Headful {
		t.Fatal("missing headful setting should migrate to visible browser")
	}
}

func TestLoadBrowserBoxPreservesExplicitHeadlessSetting(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO app_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, browserBoxKey, `{"settings":{"enabled":true,"headful":false}}`); err != nil {
		t.Fatal(err)
	}
	doc, ok, err := store.LoadBrowserBox(context.Background())
	if err != nil || !ok {
		t.Fatalf("LoadBrowserBox() = %#v, %v, %v", doc, ok, err)
	}
	if doc.Settings.Headful {
		t.Fatal("explicit headful=false must remain headless")
	}
}
