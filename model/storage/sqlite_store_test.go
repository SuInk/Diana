package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/updater"
)

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

	groupConfigs := assistant.GroupConfigSet{
		Groups: []assistant.GroupConfig{
			{
				GroupID:            "123456",
				Enabled:            true,
				GroupTriggers:      []string{"Diana"},
				WelcomeEnabled:     true,
				WelcomeMessage:     "欢迎 {user_id}",
				RecentContextLimit: 8,
				MaxReplyChars:      1200,
				PluginOverrides:    map[string]bool{"official.file-parser-go": true},
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
	if !ok || !gotGroup.PluginOverrides["official.file-parser-go"] || gotGroup.RecentContextLimit != 8 {
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
