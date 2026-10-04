package assistant

import "testing"

func TestPluginSettingSpecConfigurePolicy(t *testing.T) {
	cases := []struct {
		name  string
		spec  PluginSettingSpec
		scope string
		who   string
		want  bool
	}{
		{name: "owner can edit secret", spec: PluginSettingSpec{Key: "token", Secret: true}, scope: PluginSettingScopeBot, who: PluginSettingEditorOwner, want: true},
		{name: "group admin gets explicit group setting", spec: PluginSettingSpec{Key: "search", Scopes: []string{PluginSettingScopeGroup}, ConfigureBy: []string{PluginSettingEditorGroupAdmin}}, scope: PluginSettingScopeGroup, who: PluginSettingEditorGroupAdmin, want: true},
		{name: "member gets explicit personal setting", spec: PluginSettingSpec{Key: "format", Scopes: []string{PluginSettingScopeUser}, ConfigureBy: []string{PluginSettingEditorMember}}, scope: PluginSettingScopeUser, who: PluginSettingEditorMember, want: true},
		{name: "member cannot edit group setting", spec: PluginSettingSpec{Key: "search", Scopes: []string{PluginSettingScopeGroup}, ConfigureBy: []string{PluginSettingEditorGroupAdmin}}, scope: PluginSettingScopeGroup, who: PluginSettingEditorMember, want: false},
		{name: "global only cannot be group overridden", spec: PluginSettingSpec{Key: "port", GlobalOnly: true}, scope: PluginSettingScopeGroup, who: PluginSettingEditorGroupAdmin, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.CanConfigure(tc.scope, tc.who); got != tc.want {
				t.Fatalf("CanConfigure() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeGroupPluginSettingsRejectsNonGroupScope(t *testing.T) {
	_, err := normalizeGroupPluginSettings([]PluginSettingSpec{{Key: "format", Type: PluginSettingTypeString, Scopes: []string{PluginSettingScopeUser}}}, map[string]any{"format": "short"})
	if err == nil {
		t.Fatal("user-only setting unexpectedly accepted as group override")
	}
}
