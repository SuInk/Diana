package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type testBotAdminsSaver struct {
	ids map[string][]string
}

func (s *testBotAdminsSaver) SaveBotConfig(BotConfig) {}
func (s *testBotAdminsSaver) SaveAdminIDs(id string, ids []string) error {
	if s.ids == nil {
		s.ids = map[string][]string{}
	}
	s.ids[id] = append([]string(nil), ids...)
	return nil
}

const (
	adminTestOwner = "10001"
	adminTestAdmin = "20002"
	adminTestUser  = "30003"
	adminTestBot   = "40004"
)

func adminTestConfig() BotConfig {
	return BotConfig{ID: "a", OwnerID: adminTestOwner, BotAccount: adminTestBot, AdminIDs: []string{adminTestAdmin}, AgentEnabled: true}
}

func adminTestRuntime(t *testing.T, saver ConfigSaver) *Runtime {
	t.Helper()
	cfg := adminTestConfig()
	provider := &hostReviewStubProvider{reply: hostReviewAllowReply}
	r := NewRuntime(cfg, nilChannel{}, NewDefaultPluginManager(), nil, nil, saver, func() (LLMProvider, error) { return provider, nil })
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	return r
}

func adminTestEvent(userID string) MessageEvent {
	event := privateEvent(userID, "50005", "测试")
	event.ProfileID = "a"
	return event
}

func TestAdminIdentityIsSeparateFromOwner(t *testing.T) {
	cfg := adminTestConfig()
	for _, test := range []struct {
		user          string
		owner, admin  bool
		manager       bool
		scheduleLimit int
		speakerMarker string
	}{
		{adminTestOwner, true, false, true, 50, promptSpeakerOwner},
		{adminTestAdmin, false, true, true, 10, promptSpeakerAdmin},
		{adminTestUser, false, false, false, 10, promptSpeakerNotOwner},
	} {
		event := adminTestEvent(test.user)
		if cfg.IsOwnerEvent(event) != test.owner || cfg.IsAdminEvent(event) != test.admin || cfg.IsManagerEvent(event) != test.manager {
			t.Fatalf("%s: owner=%v admin=%v manager=%v", test.user, cfg.IsOwnerEvent(event), cfg.IsAdminEvent(event), cfg.IsManagerEvent(event))
		}
		policy := relationshipPolicyForEvent(cfg, UserMemoryProfile{}, event)
		if policy.Owner != test.owner || policy.Admin != test.admin || policy.Manages() != test.manager {
			t.Fatalf("%s: policy=%+v", test.user, policy)
		}
		// 管理员按主人装配工具，但额度和身份那一行仍按真实身份。
		if policy.toolScope().Owner != test.manager || policy.personalScheduleLimit() != test.scheduleLimit {
			t.Fatalf("%s: toolScope owner=%v limit=%d", test.user, policy.toolScope().Owner, policy.personalScheduleLimit())
		}
		if !strings.Contains(relationshipPermissionContext(policy), test.speakerMarker) {
			t.Fatalf("%s: speaker identity line missing %q", test.user, test.speakerMarker)
		}
	}
	// 机器人自己发出的消息、别的机器人配置的事件都不算管理员。
	outbound := adminTestEvent(adminTestAdmin)
	outbound.Outbound = true
	other := adminTestEvent(adminTestAdmin)
	other.ProfileID = "b"
	if cfg.IsAdminEvent(outbound) || cfg.IsAdminEvent(other) {
		t.Fatal("outbound or other-profile event counted as admin")
	}
}

func TestAdminIDsNormalizeAndRoundTrip(t *testing.T) {
	cfg := BotConfig{OwnerID: adminTestOwner, AdminIDs: []string{" 20002 ", "20002", adminTestOwner, "", "@AdminUser"}}.WithDefaults()
	if want := []string{"20002", "@AdminUser"}; !reflect.DeepEqual(cfg.AdminIDs, want) {
		t.Fatalf("admin ids = %v, want %v", cfg.AdminIDs, want)
	}
	payload := PayloadFromConfig(cfg)
	body, err := json.Marshal(payload)
	if err != nil || !strings.Contains(string(body), `"admin_ids":["20002","@AdminUser"]`) {
		t.Fatalf("payload = %s err=%v", body, err)
	}
	var decoded ConfigPayload
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFromPayload(decoded, BotConfig{}).WithDefaults().AdminIDs; !reflect.DeepEqual(got, cfg.AdminIDs) {
		t.Fatalf("round trip = %v", got)
	}
}

func TestTelegramAdminMatchesUsername(t *testing.T) {
	cfg := BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: "60006", AdminIDs: []string{"@AdminUser"}}
	event := MessageEvent{ProfileID: "tg", Platform: PlatformTelegram, UserID: "70007", SenderUsername: "adminuser"}
	if !cfg.IsAdminEvent(event) {
		t.Fatal("telegram admin username not matched")
	}
	event.SenderUsername = "someoneelse"
	if cfg.IsAdminEvent(event) {
		t.Fatal("different username matched")
	}
}

func TestAdminGetsManagementToolsButNotAdminManagement(t *testing.T) {
	r := adminTestRuntime(t, nil)
	for _, test := range []struct {
		user                        string
		host, plugins, admins, conf bool
	}{
		{adminTestOwner, true, true, true, true},
		{adminTestAdmin, true, true, false, true},
		{adminTestUser, false, false, false, false},
	} {
		event := adminTestEvent(test.user)
		policy := relationshipPolicyForEvent(r.ProfileConfig("a"), UserMemoryProfile{}, event)
		registry, err := r.newAgentRegistry(context.Background(), r.ProfileConfig("a"), event, policy)
		if err != nil {
			t.Fatal(err)
		}
		has := func(name string) bool {
			_, ok := registry.Get(name)
			return ok
		}
		if has(hostAccessToolName) != test.host || has(pluginManageToolName) != test.plugins || has(botAdminsToolName) != test.admins || has(botConfigToolName) != test.conf {
			t.Fatalf("%s: host=%v plugins=%v admins=%v config=%v", test.user, has(hostAccessToolName), has(pluginManageToolName), has(botAdminsToolName), has(botConfigToolName))
		}
		registry.Close()
	}
}

func TestAdminRunsReviewedHostCommandsAndPluginToggles(t *testing.T) {
	r := adminTestRuntime(t, nil)
	host := &hostAccessTool{runtime: r, event: adminTestEvent(adminTestAdmin)}
	if _, err := host.Run(context.Background(), map[string]any{"operation": "list", "path": t.TempDir()}); err != nil {
		t.Fatalf("admin host read: %v", err)
	}
	host.event = adminTestEvent(adminTestUser)
	if _, err := host.Run(context.Background(), map[string]any{"operation": "list", "path": t.TempDir()}); err == nil {
		t.Fatal("ordinary user read host")
	}
	plugins := &dianaPluginManageTool{runtime: r, event: adminTestEvent(adminTestAdmin)}
	if _, err := plugins.Run(context.Background(), map[string]any{"operation": "list"}); err != nil {
		t.Fatalf("admin plugin list: %v", err)
	}
	// 取消管理员后立刻失去权限：每次执行都按当前配置重新核身份。
	cfg := r.ProfileConfig("a")
	cfg.AdminIDs = nil
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err := plugins.Run(context.Background(), map[string]any{"operation": "list"}); err == nil {
		t.Fatal("removed admin kept plugin access")
	}
}

func TestBotAdminsToolOwnerOnly(t *testing.T) {
	saver := &testBotAdminsSaver{}
	r := adminTestRuntime(t, saver)
	ctx := context.Background()
	owner := &dianaBotAdminsTool{runtime: r, event: adminTestEvent(adminTestOwner)}
	out, err := owner.Run(ctx, map[string]any{"operation": "add", "user_id": adminTestUser})
	if err != nil || !strings.Contains(out, adminTestUser) {
		t.Fatalf("add: %s %v", out, err)
	}
	if want := []string{adminTestAdmin, adminTestUser}; !reflect.DeepEqual(saver.ids["a"], want) || !reflect.DeepEqual(r.ProfileConfig("a").AdminIDs, want) {
		t.Fatalf("saved=%v runtime=%v", saver.ids["a"], r.ProfileConfig("a").AdminIDs)
	}
	if !r.ProfileConfig("a").IsManagerEvent(adminTestEvent(adminTestUser)) {
		t.Fatal("new admin has no management rights")
	}
	for _, target := range []string{adminTestOwner, adminTestBot, "某个昵称"} {
		if _, err := owner.Run(ctx, map[string]any{"operation": "add", "user_id": target}); err == nil {
			t.Fatalf("added invalid admin %q", target)
		}
	}
	// 引用对方消息也能指定目标。
	quoted := adminTestEvent(adminTestOwner)
	quoted.Quoted = &QuotedMessage{UserID: adminTestUser}
	owner.event = quoted
	if _, err := owner.Run(ctx, map[string]any{"operation": "remove"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{adminTestAdmin}; !reflect.DeepEqual(saver.ids["a"], want) {
		t.Fatalf("after remove saved=%v", saver.ids["a"])
	}
	// 管理员不能增减管理员，名单也只给主人看。
	admin := &dianaBotAdminsTool{runtime: r, event: adminTestEvent(adminTestAdmin)}
	for _, op := range []string{"add", "remove", "list"} {
		if _, err := admin.Run(ctx, map[string]any{"operation": op, "user_id": adminTestUser}); err == nil {
			t.Fatalf("admin ran %s", op)
		}
	}
}
