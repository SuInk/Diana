// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 新建配置装完就能用：写入默认打开，标准模式下直接生效（安全模式下不挂，见
// agent_safe_mode_test.go）。run_command 不在默认注册表里：它跑在工作区容器里，见 boxCommandTool。
func TestDefaultBotConfigShipsUsableAgentCapabilities(t *testing.T) {
	cfg := standardModeBotConfig()
	if !cfg.AgentFileWriteEnabled {
		t.Fatal("新建配置没有打开写入")
	}
	registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"write_file", "edit_file", "grep", "find_files"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("默认配置下 %s 没有注册", name)
		}
	}
	if _, ok := registry.Get("run_command"); ok {
		t.Fatal("默认注册表里构造了宿主机上的 run_command")
	}
	member, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := member.Get("run_command"); ok {
		t.Fatal("成员的注册表里构造了 run_command")
	}
}

// forceBox 让工作区容器可不可用不取决于跑测试的机器有没有 Docker。
func forceBox(t *testing.T, available bool) {
	t.Helper()
	previous := boxAvailable
	boxAvailable = func() bool { return available }
	t.Cleanup(func() { boxAvailable = previous })
}

// run_command / box_reset 按三档开关挂：默认仅主人；Docker 不可用或安全模式时哪一档都不挂。
func TestBoxToolsFollowAccessLevelAndDocker(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	member := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30001", ProfileID: "bot-a"}
	// 档位按运行时里这台机器人的配置判断，所以每种配置各起一个运行时。
	registryHas := func(cfg BotConfig, policy RelationshipPolicy) bool {
		t.Helper()
		cfg.OwnerID = "owner"
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		defer runtime.closeAgentRegistryCache()
		registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), member, policy)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		_, command := registry.Get(boxCommandToolName)
		_, reset := registry.Get(boxResetToolName)
		if command != reset {
			t.Fatalf("run_command=%v box_reset=%v", command, reset)
		}
		return command
	}
	forceBox(t, true)
	owner, memberPolicy := RelationshipPolicy{Owner: true}, RelationshipPolicy{Score: 60}
	ownerOnly := standardModeBotConfig()
	if got := ownerOnly.WithDefaults().AgentBoxAccess; got != BoxAccessOwner {
		t.Fatalf("默认档位 = %q，想要仅主人", got)
	}
	if !registryHas(ownerOnly, owner) || registryHas(ownerOnly, memberPolicy) {
		t.Fatal("仅主人：主人应有、成员不应有 run_command")
	}
	on := standardModeBotConfig()
	on.AgentBoxAccess = BoxAccessAll
	if !registryHas(on, memberPolicy) || !registryHas(on, owner) {
		t.Fatal("所有人：成员和主人都应有 run_command")
	}
	off := standardModeBotConfig()
	off.AgentBoxAccess = BoxAccessOff
	if registryHas(off, owner) {
		t.Fatal("关闭时却挂了 run_command")
	}
	safe := on
	safe.AgentMode = AgentModeSafe
	if registryHas(safe, RelationshipPolicy{Owner: true}) {
		t.Fatal("安全模式下挂了 run_command")
	}
	forceBox(t, false)
	if registryHas(on, RelationshipPolicy{Owner: true}) {
		t.Fatal("没有 Docker 时挂了 run_command")
	}
}

// 每个机器人的每个会话一个 box；同一个群里谁说话都是同一个。
func TestBoxKeyPerBotAndSession(t *testing.T) {
	r := &Runtime{}
	a := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30001", ProfileID: "bot-a"}
	b := a
	b.UserID = "30002"
	if r.boxKey(a) != r.boxKey(b) {
		t.Fatal("同一个群的成员应共用一个 box")
	}
	other := a
	other.GroupID = "20002"
	otherBot := a
	otherBot.ProfileID = "bot-b"
	private := MessageEvent{Kind: EventKindPrivate, UserID: "30001", ProfileID: "bot-a"}
	for _, e := range []MessageEvent{other, otherBot, private} {
		if r.boxKey(e) == r.boxKey(a) {
			t.Fatalf("%+v 不该和群 20001 共用 box", e)
		}
	}
}

// 群成员只能把本群保存过的文件放进 box；路径以 /work 开头的 send_file 走 box。
func TestBoxInputsRequireGroupGrant(t *testing.T) {
	forceBox(t, true)
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	r := NewRuntime(BotConfig{OwnerID: "owner", AgentBoxAccess: BoxAccessAll}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30001", ProfileID: "bot-a"}
	tool := &boxCommandTool{runtime: r, event: event}
	if _, err := tool.stageInputs(context.Background(), nil, []string{"private/owner-notes.txt"}); err == nil {
		t.Fatal("成员把没在本群保存过的文件放进了 box")
	}
	if _, err := tool.stageInputs(context.Background(), nil, make([]string, boxMaxInputs+1)); err == nil {
		t.Fatal("inputs 超过上限没有拒绝")
	}
	if !isBoxPath("/work/out.png") || isBoxPath("/workspace/a") || isBoxPath("out.png") {
		t.Fatal("isBoxPath 判断错了")
	}
}

// 写入开关的默认值只作用于新建配置：存量部署当初关掉的，升级后仍然是关的。
func TestWithDefaultsDoesNotGrantFileWriteToStoredConfigs(t *testing.T) {
	stored := BotConfig{AgentEnabled: true, AgentFileWriteEnabled: false}.WithDefaults()
	if stored.AgentFileWriteEnabled {
		t.Fatal("存量配置的写入开关被默认值打开了")
	}
	registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(stored, MessageEvent{}, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"write_file", "edit_file"} {
		if _, ok := registry.Get(tool); ok {
			t.Fatalf("存量配置升级后凭空多出了 %s", tool)
		}
	}
}

// 命令白名单已删除：旧配置里的 agent_command_allowlist 读取时忽略（空白名单也不再
// 关掉 run_command），保存时不再输出。
func TestLegacyCommandAllowlistIsIgnored(t *testing.T) {
	for _, raw := range []string{`{"agent_command_allowlist":[]}`, `{"agent_command_allowlist":["uptime"]}`} {
		var cfg BotConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.AgentMode = AgentModeStandard
		body, err := json.Marshal(PayloadFromConfig(cfg.WithDefaults()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "agent_command_allowlist") {
			t.Fatal("removed setting still serialized")
		}
	}
}

func TestLegacyStepSettingIsIgnored(t *testing.T) {
	var cfg BotConfig
	if err := json.Unmarshal([]byte(`{"agent_max_steps":1}`), &cfg); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(cfg.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, owner := range []bool{false, true} {
		if got := runtime.agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, owner).WithDefaults().MaxSteps; got != 0 {
			t.Fatalf("owner=%v budget=%d", owner, got)
		}
	}
	body, err := json.Marshal(PayloadFromConfig(cfg.WithDefaults()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "agent_max_steps") {
		t.Fatal("removed setting still serialized")
	}
}

// 旧配置打开过「群成员可运行命令」的，升级后换算成「所有人」；没打开过的按默认仅主人。
// 迁移后旧键不再输出。
func TestLegacyMemberCommandSwitchMigratesToBoxAccess(t *testing.T) {
	for raw, want := range map[string]string{
		`{"agent_member_command_enabled":true}`:  BoxAccessAll,
		`{"agent_member_command_enabled":false}`: BoxAccessOwner,
		`{}`:                                     BoxAccessOwner,
		`{"agent_member_command_enabled":true,"agent_box_access":"off"}`:   BoxAccessOff,
		`{"agent_member_command_enabled":true,"agent_box_access":"owner"}`: BoxAccessOwner,
	} {
		var cfg BotConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		migrated := cfg.WithDefaults()
		if migrated.AgentBoxAccess != want {
			t.Fatalf("%s → %q, want %q", raw, migrated.AgentBoxAccess, want)
		}
		body, err := json.Marshal(migrated)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "agent_member_command_enabled") {
			t.Fatalf("%s: legacy key still serialized: %s", raw, body)
		}
	}
}

// 工作区磁盘上限默认 5 GB，太小的值抬到最小值。
func TestBoxDiskLimitDefaults(t *testing.T) {
	for in, want := range map[int]int{0: 5120, -1: 5120, 10: 100, 2048: 2048} {
		if got := (BotConfig{AgentBoxDiskLimitMB: in}).WithDefaults().AgentBoxDiskLimitMB; got != want {
			t.Fatalf("%d → %d, want %d", in, got, want)
		}
	}
}
