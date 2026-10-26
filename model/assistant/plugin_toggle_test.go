package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func pluginToggleTestRuntime(t *testing.T) (*Runtime, *int) {
	t.Helper()
	base := BotConfig{ID: "a", Enabled: true, OwnerID: "100001"}
	plugins := NewPluginManager()
	if err := plugins.RegisterPlugin(testPlugin{}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install("test"); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(base, nilChannel{}, plugins, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	saves := 0
	r.SetPluginStatePersister(func(context.Context) error { saves++; return nil })
	return r, &saves
}

func TestOwnerPluginCommandsToggleAndPersist(t *testing.T) {
	r, saves := pluginToggleTestRuntime(t)
	event := privateEvent("100001", "20501", "")
	event.ProfileID = "a"
	reply, handled := r.handleOwnerCommand(event, "插件 禁用 Test")
	if !handled || !strings.Contains(reply, "已停用插件「Test」") {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
	if r.plugins.EnabledWithOverrides("test", r.pluginOverridesForEvent(event)) || *saves != 1 {
		t.Fatalf("plugin still enabled or not saved (saves=%d)", *saves)
	}
	if list, _ := r.handleOwnerCommand(event, "插件 列表"); !strings.Contains(list, "Test（test）：已停用") {
		t.Fatalf("list = %q", list)
	}
	if reply, _ := r.handleOwnerCommand(event, "插件 启用 test"); !strings.Contains(reply, "已启用") || *saves != 2 {
		t.Fatalf("reply=%q saves=%d", reply, *saves)
	}
	if reply, _ := r.handleOwnerCommand(event, "插件 启用 不存在"); !strings.Contains(reply, "没有找到插件") {
		t.Fatalf("unknown plugin reply = %q", reply)
	}
	member := privateEvent("300003", "20502", "")
	member.ProfileID = "a"
	if _, handled := r.handleOwnerCommand(member, "插件 禁用 test"); handled {
		t.Fatal("non-owner toggled a plugin")
	}
}

func TestPluginManageToolOwnerOnlyAndReportsSaveFailure(t *testing.T) {
	r, _ := pluginToggleTestRuntime(t)
	event := privateEvent("100001", "20503", "")
	event.ProfileID = "a"
	tool := &dianaPluginManageTool{runtime: r, event: event}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	var listed struct{ Plugins []pluginListItem }
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed.Plugins) != 1 || !listed.Plugins[0].Effective {
		t.Fatalf("list=%s err=%v", out, err)
	}
	r.SetPluginStatePersister(func(context.Context) error { return errors.New("disk full") })
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "disable", "plugin": "test"}); err == nil || !strings.Contains(err.Error(), "保存失败") {
		t.Fatalf("save failure hidden: %v", err)
	}
	tool.event.UserID = "300003"
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "list"}); err == nil {
		t.Fatal("non-owner listed plugins")
	}
}
