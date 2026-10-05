package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Opt-in replay with the current production model, persona, plugin settings and
// historical request. Delivery, group settings and tasks use disposable stores.
// Host-local skills and MCP processes are not started on the replay machine.
func TestLiveWelcomeConfigReplay(t *testing.T) {
	path := os.Getenv("DIANA_WELCOME_REPLAY_BUNDLE")
	if path == "" {
		t.Skip("set DIANA_WELCOME_REPLAY_BUNDLE to a private production replay bundle")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		BotConfig    BotConfig
		GroupConfig  GroupConfig
		Profiles     llm.ProfileSet
		Registry     llm.ProviderRegistryDocument
		PluginStates map[string]PersistedPluginState
		Event        MessageEvent
		History      []MessageEvent
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal("invalid private replay bundle")
	}
	if bundle.BotConfig.ID == "" || bundle.Event.GroupID == "" || len(bundle.History) == 0 || len(bundle.PluginStates) == 0 {
		t.Fatal("requires production config, plugins, original event and history")
	}
	registry, err := llm.RegistryFromDocument(bundle.Registry)
	if err != nil {
		t.Fatal("cannot initialize production model registry")
	}
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "replay.db"))
	cfg := bundle.BotConfig.WithDefaults()
	cfg.AgentSkillRoots, cfg.AgentMCPConfigPath = nil, ""
	cfg.AgentFileWriteEnabled = false
	cfg.DebugModeEnabled = boolPointer(true)
	cfg.RequestTimeout = 4 * time.Minute
	plugins := NewDefaultPluginManager()
	plugins.Restore(bundle.PluginStates)
	channel := &recordingChannel{}
	tasks := &stubReminderStore{}
	logs := &captureAppLogs{}
	saver := &participationToolSaver{cfg: cfg}
	r := NewRuntime(cfg, channel, plugins, &stubLLMProfileStore{set: bundle.Profiles}, tasks, saver, nil)
	r.SetLLMProviderRegistry(registry)
	r.SetAppLogWriter(logs)
	defer r.closeAgentRegistryCache()
	group := bundle.GroupConfig
	if group.GroupID == "" {
		group = DefaultGroupConfig(bundle.Event.GroupID, cfg)
	}
	group.BotProfileID = cfg.ID
	group.WelcomeEnabled = boolPointer(false)
	groups := &testWritableGroupConfigStore{set: GroupConfigSet{Groups: []GroupConfig{group}}}
	r.SetGroupConfigStore(groups)
	at := time.Unix(bundle.Event.Time, 0)
	r.now = func() time.Time { return at }
	for _, event := range bundle.History {
		r.remember(soulReplayWithoutImages(event))
	}
	beforeParticipation := r.effectiveConfigForEvent(bundle.Event).participationPreferences()
	beforeBotWelcome := welcomeSettingsFromConfig(r.ProfileConfig(cfg.ID))
	cases := []struct {
		name, question string
		enabled        bool
		fixed          bool
		join           bool
		participation  bool
	}{
		{name: "original_enable", enabled: true, join: true},
		{name: "repeat_enable", question: "本群入群欢迎再开一次", enabled: true},
		{name: "read_group_settings", question: "查一下本群现在实际生效的入群欢迎设置", enabled: true},
		{name: "owner_diagnostics", question: "读取我的完整机器人配置和运行状态", enabled: true},
		{name: "fixed_message", question: "把本群欢迎模式改成固定，欢迎词设为「欢迎 {user_id}」，其他设置保持原样", enabled: true, fixed: true},
		{name: "disable", question: "关闭本群入群欢迎", enabled: false, fixed: true, join: true},
		{name: "participation", question: "把本群回应提问打开，闲聊档位设为低，主动闲聊冷却改成60秒", enabled: false, fixed: true, participation: true},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := soulReplayWithoutImages(bundle.Event)
			event.MessageID = fmt.Sprintf("welcome-replay-%s-%d", tc.name, index)
			at = at.Add(time.Second)
			event.Time = at.Unix()
			if tc.question != "" {
				event.RawMessage = tc.question
				event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": tc.question}}}
			}
			r.remember(event)
			start := len(logs.entriesSnapshot())
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			ctx = r.withDebugTraceContext(ctx, event)
			reply, runErr := r.replyTo(ctx, event, event.RawMessage)
			cancel()
			var steps []agent.Step
			models := map[string]bool{}
			for _, entry := range logs.entriesSnapshot()[start:] {
				if entry.Action != "debug_trace" {
					continue
				}
				phase, _ := entry.Metadata["phase"].(string)
				if phase == "model_request" {
					if model, ok := entry.Metadata["model"].(string); ok && model != "" && entry.Metadata["error"] == nil {
						models[model] = true
					}
				}
				if phase != "agent_"+string(agent.RunPhaseToolCompleted) {
					continue
				}
				tool, _ := entry.Metadata["tool"].(string)
				input, _ := entry.Metadata["tool_input"].(map[string]any)
				output, _ := entry.Metadata["tool_output"].(string)
				stepErr, _ := entry.Metadata["error"].(string)
				steps = append(steps, agent.Step{Tool: tool, Input: input, Output: output, Error: stepErr})
			}
			modelNames := make([]string, 0, len(models))
			for model := range models {
				modelNames = append(modelNames, model)
			}
			sort.Strings(modelNames)
			actual := r.effectiveConfigForEvent(event)
			result := map[string]any{"case": tc.name, "request": event.RawMessage, "models": modelNames, "reply": reply, "steps": steps,
				"welcome": welcomeSettingsFromConfig(actual), "tasks": len(tasks.Reminders())}
			if runErr != nil {
				result["error"] = runErr.Error()
			}
			if dir := os.Getenv("DIANA_WELCOME_REPLAY_RESULTS"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.MarshalIndent(result, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, tc.name+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("models=%s reply=%s", strings.Join(modelNames, ","), reply)
			if runErr != nil {
				t.Fatal(runErr)
			}
			configCalls := 0
			for _, step := range steps {
				input, _ := json.Marshal(step.Input)
				t.Logf("CALL %s %s error=%s", step.Tool, input, step.Error)
				if step.Error != "" {
					t.Errorf("tool returned a configuration or execution error: %s: %s", step.Tool, step.Error)
				}
				if step.Tool == "event_trigger" || step.Tool == "config" {
					t.Errorf("selected removed or duplicate-welcome tool: %s", step.Tool)
				}
				if step.Tool == botConfigToolName {
					configCalls++
				}
			}
			if configCalls == 0 {
				t.Error("did not check or update the unified configuration tool")
			}
			if actual.WelcomeEnabled != tc.enabled || len(tasks.Reminders()) != 0 {
				t.Fatalf("welcome=%t expected=%t tasks=%d", actual.WelcomeEnabled, tc.enabled, len(tasks.Reminders()))
			}
			if tc.fixed && (actual.WelcomeMode != WelcomeModeFixed || actual.WelcomeMessage != "欢迎 {user_id}") {
				t.Errorf("fixed welcome was not saved exactly: %+v", welcomeSettingsFromConfig(actual))
			}
			wantParticipation := beforeParticipation
			if tc.participation {
				wantParticipation.RelevanceLevel, wantParticipation.ChatLevel, wantParticipation.CooldownSeconds = "on", "low", 60
			}
			if !reflect.DeepEqual(wantParticipation, actual.participationPreferences()) || !reflect.DeepEqual(beforeBotWelcome, welcomeSettingsFromConfig(r.ProfileConfig(cfg.ID))) {
				t.Error("incorrect participation update or changed bot defaults")
			}
			if tc.join {
				before := len(channel.sentSnapshot())
				join := MessageEvent{ProfileID: cfg.ID, Platform: event.Platform, SelfID: event.SelfID, Kind: EventKindNotice,
					SubType: "group_increase", GroupID: event.GroupID, UserID: "welcome-replay-new-member", SenderName: "回放新成员", Time: at.Unix()}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				if err := r.HandleEvent(ctx, join); err != nil {
					t.Fatal(err)
				}
				sends, expected := len(channel.sentSnapshot())-before, 0
				if tc.enabled {
					expected = 1
				}
				t.Logf("member_join sent=%d expected=%d", sends, expected)
				if sends != expected {
					t.Errorf("member_join sent=%d expected=%d", sends, expected)
				}
			}
		})
	}
}
