package webui

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/browserctl"
)

func TestConsoleConfigCatalogListsGlobalSwitches(t *testing.T) {
	ctx := context.Background()
	registry := browserctl.NewRegistry(ctx, nil)
	m := NewConsoleConfigManager(nil, nil, nil, nil, nil, nil)
	m.SetSwitchHandlers(ConsoleSwitchHandlers{
		BrowserControl: NewBrowserControlHandler(registry, browserctl.NewHub(registry)),
		SystemUpdate:   NewSystemUpdateHandler(nil),
	})
	catalog := m.Catalog().(map[string]any)
	for _, key := range []string{"browser_control", "update_policy", "group_style"} {
		if _, ok := catalog[key]; !ok {
			t.Fatalf("catalog 缺少 %s：%v", key, catalog)
		}
	}
	// 没注入的页不列，免得模型去改一个不存在的设置。
	if _, ok := catalog["desktop_control"]; ok {
		t.Fatal("没注入桌面控制却列了 desktop_control")
	}
	if _, err := m.Read(ctx, "bot", "desktop_control", ""); err == nil {
		t.Fatal("没注入桌面控制却能读")
	}
}

func TestConsoleConfigBrowserControlPatchKeepsOtherFields(t *testing.T) {
	ctx := context.Background()
	registry := browserctl.NewRegistry(ctx, nil)
	if _, err := registry.SetPolicy(ctx, browserctl.Policy{Enabled: false, AllowedHosts: []string{"example.com"}, WriteEnabled: true}); err != nil {
		t.Fatal(err)
	}
	m := NewConsoleConfigManager(nil, nil, nil, nil, nil, nil)
	m.SetSwitchHandlers(ConsoleSwitchHandlers{BrowserControl: NewBrowserControlHandler(registry, browserctl.NewHub(registry))})
	if _, err := m.Apply(ctx, "bot", "browser_control", "update", "", map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	got := registry.Policy()
	if !got.Enabled || !got.WriteEnabled || len(got.AllowedHosts) != 1 || got.AllowedHosts[0] != "example.com" {
		t.Fatalf("补丁之外的字段被改了：%+v", got)
	}
	if _, err := m.Apply(ctx, "bot", "browser_control", "update", "", map[string]any{"no_such_key": true}); err == nil {
		t.Fatal("接受了不存在的字段")
	}
}

func TestConsoleConfigUpdatePolicyPatch(t *testing.T) {
	ctx := context.Background()
	h := NewSystemUpdateHandler(nil)
	m := NewConsoleConfigManager(nil, nil, nil, nil, nil, nil)
	m.SetSwitchHandlers(ConsoleSwitchHandlers{SystemUpdate: h})
	before := h.currentPolicy()
	if _, err := m.Apply(ctx, "bot", "update_policy", "update", "", map[string]any{"auto_install": true}); err != nil {
		t.Fatal(err)
	}
	got := h.currentPolicy()
	if !got.AutoInstall || got.Channel != before.Channel || got.AutoDownload != before.AutoDownload {
		t.Fatalf("自动更新策略补丁不对：%+v", got)
	}
	if _, err := m.Apply(ctx, "bot", "update_policy", "update", "", map[string]any{"channel": "nightly"}); err == nil {
		t.Fatal("接受了不存在的更新通道")
	}
}
