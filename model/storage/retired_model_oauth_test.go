// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSQLiteStoreRemovesRetiredModelOAuthState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"llm_oauth":             `{"tokens":{"example":{"access_token":"retired-access-token","refresh_token":"retired-refresh-token"}},"providers":[{"key":"example","client_secret":"retired-secret"}]}`,
		llmProfilesKey:          `{"profiles":[{"id":"login","config":{"api_key":"preserved-api-key","oauth_provider":"example","headers":{"oauth_provider":"unrelated-header"}}},{"id":"key-only","config":{"api_key":"another-api-key"}}]}`,
		llmRegistryKey:          `{"version":1,"providers":[{"id":"login","apiKey":"preserved-api-key","oauthProvider":"example"},{"id":"key-only","apiKey":"another-api-key"}],"models":[]}`,
		webuiAuthKey:            `{"password_hash":"preserved-password-hash"}`,
		webuiSessionsKey:        `{"sessions":[{"token":"preserved-session"}]}`,
		"trusted_app_test_data": `{"oauth_provider":"unrelated-authentication","token":"preserved-app-token"}`,
	}
	for key, value := range fixtures {
		if _, err := db.Exec(`INSERT INTO app_state (key, value) VALUES (?, ?)`, key, value); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM app_state WHERE key = 'llm_oauth'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired login document: count=%d err=%v", count, err)
	}
	want := map[string]string{
		llmProfilesKey:          `{"profiles":[{"id":"login","config":{"api_key":"preserved-api-key","headers":{"oauth_provider":"unrelated-header"}}},{"id":"key-only","config":{"api_key":"another-api-key"}}]}`,
		llmRegistryKey:          `{"version":1,"providers":[{"id":"login","apiKey":"preserved-api-key"},{"id":"key-only","apiKey":"another-api-key"}],"models":[]}`,
		webuiAuthKey:            fixtures[webuiAuthKey],
		webuiSessionsKey:        fixtures[webuiSessionsKey],
		"trusted_app_test_data": fixtures["trusted_app_test_data"],
	}
	for attempt := 0; attempt < 2; attempt++ {
		for key, rawWant := range want {
			var rawGot string
			if err := store.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, key).Scan(&rawGot); err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal([]byte(rawGot), &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(rawWant), &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("migration changed unrelated fields in %s", key)
			}
		}
		if err := store.removeLLMOAuthState(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveLLMOAuthStatePreservesMixedJSONArrays(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// These arrays contain values rejected by the typed configuration reader.
	// Removing retired fields must preserve them for that reader to diagnose.
	const mixed = `null,"plain text",true,false,42,1.25,["nested",false]`
	for _, tc := range []struct {
		name, key, input, want string
	}{
		{
			name:  "profiles with binding",
			key:   llmProfilesKey,
			input: `{"profiles":[` + mixed + `,{"id":"login","config":{"oauth_provider":"example","api_key":"preserved-key"}}],"other":true}`,
			want:  `{"profiles":[` + mixed + `,{"id":"login","config":{"api_key":"preserved-key"}}],"other":true}`,
		},
		{
			name:  "providers with binding",
			key:   llmRegistryKey,
			input: `{"providers":[` + mixed + `,{"id":"login","oauthProvider":null,"apiKey":"preserved-key"}],"models":[{"id":"preserved-model"}]}`,
			want:  `{"providers":[` + mixed + `,{"id":"login","apiKey":"preserved-key"}],"models":[{"id":"preserved-model"}]}`,
		},
		{
			name:  "profiles without binding",
			key:   llmProfilesKey,
			input: `{"profiles":[` + mixed + `,{"config":{"api_key":"preserved-key"}}]}`,
			want:  `{"profiles":[` + mixed + `,{"config":{"api_key":"preserved-key"}}]}`,
		},
		{
			name:  "providers without binding",
			key:   llmRegistryKey,
			input: `{"providers":[` + mixed + `,{"apiKey":"preserved-key"}],"models":[]}`,
			want:  `{"providers":[` + mixed + `,{"apiKey":"preserved-key"}],"models":[]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.db.Exec(`INSERT OR REPLACE INTO app_state (key, value) VALUES (?, ?)`, tc.key, tc.input); err != nil {
				t.Fatal(err)
			}
			if err := store.removeLLMOAuthState(); err != nil {
				t.Fatal(err)
			}
			var rawGot string
			if err := store.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, tc.key).Scan(&rawGot); err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal([]byte(rawGot), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("migration changed mixed array values or unrelated document fields")
			}
		})
	}
}
