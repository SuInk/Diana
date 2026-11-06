package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// toolProbePlugin 和 settingsProbePlugin 同一套设置，但 ID 带前缀、还贡献一个工具，
// 用来复现模型拿工具名（github）去问 official.repository-publish 的情形。
type toolProbePlugin struct{ settingsProbePlugin }

func (p *toolProbePlugin) Manifest() PluginManifest {
	manifest := p.settingsProbePlugin.Manifest()
	manifest.ID = "acme.tool-probe"
	manifest.Name = "Tool Probe"
	return manifest
}

func (*toolProbePlugin) AgentTools() []agent.Tool {
	return []agent.Tool{&scopeTestTool{name: "probe_tool"}}
}

func TestBotConfigResolvesPluginByToolNameOrName(t *testing.T) {
	base := BotConfig{ID: "a", Enabled: true, OwnerID: "owner"}
	plugins := NewPluginManager()
	if err := plugins.RegisterPlugin(&toolProbePlugin{}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install("acme.tool-probe"); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(base, nilChannel{}, plugins, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	r.SetPluginStatePersister(func(context.Context) error { return nil })
	group := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	run := func(input map[string]any) (string, error) {
		return newDianaBotConfigTool(r, group).Run(context.Background(), input)
	}

	for _, name := range []string{"acme.tool-probe", "tool-probe", "Tool Probe", "probe_tool"} {
		out, err := run(map[string]any{"operation": "get", "plugin": name})
		if err != nil {
			t.Fatalf("get %q: %v", name, err)
		}
		if !strings.Contains(out, `"id":"acme.tool-probe"`) {
			t.Fatalf("get %q resolved wrong plugin: %s", name, out)
		}
	}

	// 群里主人用工具名改机器人级设置，落到插件 ID 上。
	if _, err := run(map[string]any{"operation": "update", "scope": "bot", "plugin_settings": map[string]any{"probe_tool": map[string]any{"limit": 9}}}); err != nil {
		t.Fatal(err)
	}
	if state, _ := plugins.Get("acme.tool-probe"); state.Settings["limit"] == nil {
		t.Fatalf("bot setting not saved under plugin ID: %+v", state.Settings)
	}

	// 找不到时列出可配置插件，而不是说「不可在聊天里配置」。
	_, err := run(map[string]any{"operation": "get", "plugin": "github"})
	if err == nil || !strings.Contains(err.Error(), "acme.tool-probe") || strings.Contains(err.Error(), "不可在聊天里配置") {
		t.Fatalf("unknown plugin error = %v", err)
	}
}

func TestConsoleConfigWorksForOwnerInGroup(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.ID = "b"
	cfg.OwnerID = "owner"
	cfg.AgentMode = AgentModeStandard
	plugins := NewPluginManager()
	if err := plugins.RegisterPlugin(&toolProbePlugin{}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install("acme.tool-probe"); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(cfg, nilChannel{}, plugins, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	manager := &recordingConsoleConfigManager{}
	r.SetConsoleConfigManager(manager)
	event := MessageEvent{ProfileID: "b", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner", MessageID: "1"}
	run := func(e MessageEvent, in map[string]any) (string, error) {
		return (&dianaConsoleConfigTool{runtime: r, event: e}).Run(context.Background(), in)
	}
	stage := map[string]any{"operation": "stage", "resource": "plugin", "action": "update", "target": "probe_tool", "payload": map[string]any{"settings": map[string]any{"token": "new-token"}}}

	member := event
	member.UserID = "member"
	if _, err := run(member, stage); err == nil {
		t.Fatal("group member staged console config")
	}
	if _, err := run(event, stage); err != nil {
		t.Fatal(err)
	}
	// 群友替主人回「确认」不算数。
	member.MessageID, member.RawMessage = "2", "确认"
	if _, err := run(member, map[string]any{"operation": "confirm"}); err == nil {
		t.Fatal("member confirmed owner's pending change")
	}
	event.MessageID, event.RawMessage = "3", "确认"
	if _, err := run(event, map[string]any{"operation": "confirm"}); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 || manager.target != "acme.tool-probe" {
		t.Fatalf("applied %d times to %q", manager.calls, manager.target)
	}
}

type recordingConsoleConfigManager struct {
	testConsoleConfigManager
	target string
}

func (m *recordingConsoleConfigManager) Apply(ctx context.Context, profile, resource, action, target string, p map[string]any) (any, error) {
	m.target = target
	return m.testConsoleConfigManager.Apply(ctx, profile, resource, action, target, p)
}
