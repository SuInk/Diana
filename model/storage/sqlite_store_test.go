package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/updater"
)

func TestSQLiteStoreMigratesLegacyDatabaseFilename(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "diana-qq-bot.db")
	canonicalPath := filepath.Join(directory, "diana.db")
	db, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_marker (value TEXT); INSERT INTO legacy_marker(value) VALUES ('preserved')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	wantPath, err := filepath.Abs(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if store.Path() != wantPath {
		t.Fatalf("store path=%q, want %q", store.Path(), wantPath)
	}
	var value string
	if err := store.db.QueryRow(`SELECT value FROM legacy_marker`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("migrated value=%q err=%v", value, err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy database still exists: %v", err)
	}
}

func TestSQLiteStoreRejectsTwoPopulatedDatabaseNames(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "diana-qq-bot.db")
	canonicalPath := filepath.Join(directory, "diana.db")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLiteStore(legacyPath); err == nil || !strings.Contains(err.Error(), "both legacy SQLite database") {
		t.Fatalf("conflict error=%v", err)
	}
}

func TestRenameSQLiteFamilyMovesWALSidecars(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "diana-qq-bot.db")
	target := filepath.Join(directory, "diana.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(source+suffix, []byte("content"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameSQLiteFamily(source, target); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(source + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source sidecar %q still exists: %v", suffix, err)
		}
		content, err := os.ReadFile(target + suffix)
		if err != nil || string(content) != "content"+suffix {
			t.Fatalf("target sidecar %q content=%q err=%v", suffix, content, err)
		}
	}
}

// TestSQLiteStorePersistsConfigsAndPluginStates 验证对应功能场景。
func TestSQLiteStorePersistsConfigsAndPluginStates(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	llmCfg := llm.ProviderConfig{
		Provider:        llm.ProviderOpenAICompatible,
		APIKey:          "secret-key",
		BaseURL:         "https://example.com/v1",
		Model:           "gp5.5",
		ImageModel:      "gpt-image-1",
		UserAgent:       "codex-cli/0.142.0",
		Headers:         map[string]string{"X-Relay": "earlyso"},
		MaxOutputTokens: 512,
	}
	if err := store.SaveLLMConfig(ctx, llmCfg); err != nil {
		t.Fatal(err)
	}
	gotLLM, ok, err := store.LoadLLMConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadLLMConfig() ok=%v err=%v", ok, err)
	}
	if gotLLM.APIKey != "secret-key" || gotLLM.Model != "gp5.5" || gotLLM.UserAgent != "codex-cli/0.142.0" || gotLLM.Headers["X-Relay"] != "earlyso" {
		t.Fatalf("gotLLM = %#v", gotLLM)
	}

	botCfg := assistant.BotConfig{
		Enabled:                 true,
		OneBotReverseWSEndpoint: "ws://127.0.0.1:18080/onebot/v11/ws",
		OneBotAccessToken:       "1234567890abcdef",
		NoneBotBridgeToken:      "fedcba0987654321",
		BotQQ:                   "123456",
		RequestTimeout:          30 * time.Second,
	}.WithDefaults()
	if err := store.SaveQQBotConfig(ctx, botCfg); err != nil {
		t.Fatal(err)
	}
	gotBot, ok, err := store.LoadQQBotConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadQQBotConfig() ok=%v err=%v", ok, err)
	}
	if gotBot.OneBotAccessToken != botCfg.OneBotAccessToken || gotBot.BotQQ != botCfg.BotQQ {
		t.Fatalf("gotBot = %#v", gotBot)
	}

	naturalInterjectionEnabled := true
	groupConfigs := assistant.GroupConfigSet{
		Groups: []assistant.GroupConfig{
			{
				GroupID:                    "123456",
				Enabled:                    true,
				GroupTriggers:              []string{"Diana"},
				WelcomeEnabled:             true,
				WelcomeMessage:             "欢迎 {user_id}",
				RecentContextLimit:         8,
				MaxReplyChars:              1200,
				NaturalInterjectionEnabled: &naturalInterjectionEnabled,
				PluginOverrides:            map[string]bool{"official.file-parser-go": true},
				PluginSettingOverrides: assistant.PluginSettingOverrides{
					"official.file-parser-go": {"max_file_kb": float64(512)},
				},
			},
		},
	}
	if err := store.SaveQQBotGroupConfigs(ctx, groupConfigs); err != nil {
		t.Fatal(err)
	}
	gotGroupConfigs, ok, err := store.LoadQQBotGroupConfigs(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadQQBotGroupConfigs() ok=%v err=%v", ok, err)
	}
	gotGroup, ok := gotGroupConfigs.ConfigForGroup("123456")
	if !ok || !gotGroup.PluginOverrides["official.file-parser-go"] || gotGroup.PluginSettingOverrides["official.file-parser-go"]["max_file_kb"] != float64(512) || gotGroup.RecentContextLimit != 8 || gotGroup.NaturalInterjectionEnabled == nil || !*gotGroup.NaturalInterjectionEnabled {
		t.Fatalf("gotGroupConfigs = %#v", gotGroupConfigs)
	}

	pluginStates := map[string]assistant.PluginState{
		"official.file-parser-go": {
			Manifest:  assistant.PluginManifest{ID: "official.file-parser-go"},
			Installed: true,
			Enabled:   false,
		},
	}
	if err := store.SavePluginStates(ctx, pluginStates); err != nil {
		t.Fatal(err)
	}
	gotStates, ok, err := store.LoadPluginStates(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadPluginStates() ok=%v err=%v", ok, err)
	}
	if gotStates["official.file-parser-go"].Enabled {
		t.Fatalf("gotStates = %#v", gotStates)
	}

	profiles := llm.NewProfileSet(llmCfg)
	profiles.Profiles[0].Description = "主配置"
	profiles.Profiles = append(profiles.Profiles, llm.Profile{
		ID:          "secondary",
		Name:        "备用",
		Description: "备用配置",
		UpdatedAt:   time.Now().Add(-time.Hour),
		Config: llm.ProviderConfig{
			Provider: llm.ProviderAnthropic,
			APIKey:   "anthropic-key",
			Model:    "claude-sonnet-4-5",
		},
	})
	profiles.ActiveID = "secondary"
	if err := store.SaveLLMProfiles(ctx, profiles); err != nil {
		t.Fatal(err)
	}
	gotProfiles, ok, err := store.LoadLLMProfiles(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadLLMProfiles() ok=%v err=%v", ok, err)
	}
	if gotProfiles.ActiveID != "secondary" || len(gotProfiles.Profiles) != 2 {
		t.Fatalf("gotProfiles = %#v", gotProfiles)
	}
	if gotProfiles.Profiles[0].Config.Headers["X-Relay"] != "earlyso" || gotProfiles.Profiles[1].Description != "备用配置" || gotProfiles.Profiles[1].UpdatedAt.IsZero() {
		t.Fatalf("gotProfiles metadata = %#v", gotProfiles.Profiles[1])
	}

	reminders := []assistant.Reminder{
		{
			ID:                    "r1",
			OwnerID:               "10001",
			UserID:                "10001",
			Message:               "记得喝水",
			TriggerAt:             time.Now().Add(5 * time.Minute),
			LastFailureStage:      "polling",
			LastErrorFingerprint:  "fingerprint-1",
			FailureAlertedAt:      time.Now().Add(-time.Minute),
			RecoveryNoticePending: true,
			CreatedAt:             time.Now(),
		},
	}
	if err := store.SaveReminders(ctx, reminders); err != nil {
		t.Fatal(err)
	}
	gotReminders, ok, err := store.LoadReminders(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadReminders() ok=%v err=%v", ok, err)
	}
	if len(gotReminders) != 1 || gotReminders[0].Message != "记得喝水" || gotReminders[0].LastFailureStage != "polling" || gotReminders[0].LastErrorFingerprint != "fingerprint-1" || gotReminders[0].FailureAlertedAt.IsZero() || !gotReminders[0].RecoveryNoticePending {
		t.Fatalf("gotReminders = %#v", gotReminders)
	}
}

func TestSQLiteStoreUpdatePolicyRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "update-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if policy, ok, err := store.LoadUpdatePolicy(context.Background()); err != nil || ok || policy != (updater.UpdatePolicy{}) {
		t.Fatalf("LoadUpdatePolicy() before save = %#v, %v, %v", policy, ok, err)
	}
	want := updater.UpdatePolicy{AutoDownload: true, AutoInstall: false}
	if err := store.SaveUpdatePolicy(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadUpdatePolicy(context.Background())
	if err != nil || !ok || got != want {
		t.Fatalf("LoadUpdatePolicy() = %#v, %v, %v; want %#v", got, ok, err, want)
	}
}

func TestSQLiteStoreReleaseCacheRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "release-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if payload, ok, err := store.LoadReleaseCache(context.Background()); err != nil || ok || len(payload) != 0 {
		t.Fatalf("LoadReleaseCache() before save = %q, %v, %v", payload, ok, err)
	}
	want := []byte(`{"key":"SuInk/Diana?per_page=10","fetched_at":"2026-08-15T12:00:00Z"}`)
	if err := store.SaveReleaseCache(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadReleaseCache(context.Background())
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("LoadReleaseCache() = %q, %v, %v; want %q", got, ok, err, want)
	}
	if err := store.SaveReleaseCache(context.Background(), []byte(`{"broken"`)); err == nil {
		t.Fatal("SaveReleaseCache() accepted invalid JSON")
	}
}

// TestSQLiteStorePersistsAppLogs 验证对应功能场景。
func TestSQLiteStorePersistsAppLogs(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	oldAt := time.Date(2026, 6, 27, 8, 0, 0, 0, time.UTC)
	newAt := oldAt.Add(time.Minute)
	if err := store.AppendLog(ctx, AppLogEntry{
		ID:        "op-old",
		Kind:      LogKindOperation,
		Action:    "assistant.start",
		Message:   "started",
		Target:    "bot",
		CreatedAt: oldAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendLog(ctx, AppLogEntry{
		ID:        "err-new",
		Kind:      LogKindError,
		Action:    "llm.test",
		Message:   "failed",
		Detail:    "bad gateway",
		Metadata:  map[string]any{"provider": "openai_compatible", "count": 2},
		CreatedAt: newAt,
	}); err != nil {
		t.Fatal(err)
	}

	all, err := store.ListLogs(ctx, AppLogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "err-new" || all[1].ID != "op-old" {
		t.Fatalf("all logs = %#v", all)
	}
	if all[0].Level != LogLevelError || all[0].Metadata["provider"] != "openai_compatible" {
		t.Fatalf("error log = %#v", all[0])
	}

	operations, err := store.ListLogs(ctx, AppLogFilter{Kind: LogKindOperation, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Action != "assistant.start" {
		t.Fatalf("operation logs = %#v", operations)
	}
}
