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

func TestPluginManageToolTogglesAndPersists(t *testing.T) {
	r, saves := pluginToggleTestRuntime(t)
	event := privateEvent("100001", "20501", "把 Test 插件关了")
	event.ProfileID = "a"
	tool := &dianaPluginManageTool{runtime: r, event: event}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "disable", "plugin": "Test"}); err != nil {
		t.Fatal(err)
	}
	if r.plugins.EnabledWithOverrides("test", r.pluginOverridesForEvent(event)) || *saves != 1 {
		t.Fatalf("plugin still enabled or not saved (saves=%d)", *saves)
	}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "enable", "plugin": "test"})
	if err != nil || !strings.Contains(out, `"enabled":true`) || *saves != 2 {
		t.Fatalf("out=%s err=%v saves=%d", out, err, *saves)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "enable", "plugin": "不存在"}); err == nil || !strings.Contains(err.Error(), "没有找到插件") {
		t.Fatalf("unknown plugin: %v", err)
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
