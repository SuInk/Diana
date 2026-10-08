package agent

import (
	"context"
	"testing"
)

// 「全部机器人」视图下的 skill 开关：统一写到每台，列表给出几台开着，之后单台还能单独改。
func TestExtensionAdminSkillEnabledForAllProfiles(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	ctx := context.Background()
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "skill", Name: "demo", Content: "---\nname: demo\ndescription: Demo skill\n---\nHello"}); err != nil {
		t.Fatal(err)
	}
	profiles := []string{"a", "b"}
	counts := func() (int, int) {
		t.Helper()
		result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "list", ProfileIDs: profiles})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.(map[string]any)["items"].([]ExtensionState) {
			if item.Kind == ExtensionKindSkill && item.Name == "demo" {
				if item.EnabledProfiles == nil || item.ProfileCount == nil {
					t.Fatal("all-bots list missing profile counts")
				}
				return *item.EnabledProfiles, *item.ProfileCount
			}
		}
		t.Fatal("skill missing")
		return 0, 0
	}
	enabled := func(profile string, value bool) ExtensionAdminRequest {
		return ExtensionAdminRequest{Operation: "enabled", Kind: "skill", Name: "demo", ProfileID: profile, ProfileIDs: profiles, Enabled: value}
	}
	if on, total := counts(); on != 2 || total != 2 {
		t.Fatalf("default = %d/%d, want 2/2", on, total)
	}
	if _, err := AdministerExtensions(ctx, cfg, enabled("a", false)); err != nil {
		t.Fatal(err)
	}
	if on, _ := counts(); on != 1 {
		t.Fatalf("partial = %d, want 1", on)
	}
	if _, err := AdministerExtensions(ctx, cfg, enabled("", false)); err != nil {
		t.Fatal(err)
	}
	if on, _ := counts(); on != 0 {
		t.Fatalf("after disable all = %d, want 0", on)
	}
	if _, err := AdministerExtensions(ctx, cfg, enabled("b", true)); err != nil {
		t.Fatal(err)
	}
	b, _ := LoadExtensionOverrides(cfg.WorkDir, "b")
	a, _ := LoadExtensionOverrides(cfg.WorkDir, "a")
	if !b["skill:demo"] || a["skill:demo"] {
		t.Fatalf("single-bot switch after unified disable: a=%v b=%v", a, b)
	}
	if _, err := AdministerExtensions(ctx, cfg, enabled("", true)); err != nil {
		t.Fatal(err)
	}
	if on, _ := counts(); on != 2 {
		t.Fatalf("after enable all = %d, want 2", on)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "skill", Name: "demo", Enabled: true}); err == nil {
		t.Fatal("unified switch without any robot should fail")
	}
}
