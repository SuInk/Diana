package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func resolverManager(t *testing.T) *PluginManager {
	t.Helper()
	return NewPluginManager(NewResolverPlugin(nil))
}

// 凭据明文绝不能出现在读接口里。
func TestRedactedHidesSecretValues(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingDouyinCookie: "sessionid=super-secret",
		resolverSettingMaxImages:    float64(4),
	}); err != nil {
		t.Fatalf("保存设置失败：%v", err)
	}

	state, _ := manager.Get(resolverPluginID)
	redacted := state.Redacted()

	if _, leaked := redacted.Settings[resolverSettingDouyinCookie]; leaked {
		t.Fatal("脱敏后仍带着凭据明文")
	}
	if !redacted.SecretsConfigured[resolverSettingDouyinCookie] {
		t.Fatal("已配置的凭据应标记为 true")
	}
	if redacted.Settings[resolverSettingMaxImages] != float64(4) {
		t.Fatal("非凭据设置不该被抹掉")
	}

	// 序列化后整段 JSON 里都不该出现明文。
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("响应体泄漏了凭据明文：%s", encoded)
	}
}

func TestRedactedMarksUnconfiguredSecrets(t *testing.T) {
	manager := resolverManager(t)
	state, _ := manager.Get(resolverPluginID)
	redacted := state.Redacted()

	configured, present := redacted.SecretsConfigured[resolverSettingXHSCookie]
	if !present {
		t.Fatal("未配置的凭据也应出现在 SecretsConfigured 里")
	}
	if configured {
		t.Fatal("未配置的凭据应标记为 false")
	}
}

// 前端拿到的是脱敏后的空串，回传时不能把已存的凭据抹掉——
// 否则用户改一下超时时间就把 Cookie 弄丢了。
func TestUpdateSettingsKeepsSecretOnEmptySubmit(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingDouyinCookie: "keep-me",
	}); err != nil {
		t.Fatalf("首次保存失败：%v", err)
	}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingDouyinCookie: "",
		resolverSettingMaxImages:    float64(6),
	}); err != nil {
		t.Fatalf("二次保存失败：%v", err)
	}

	state, _ := manager.Get(resolverPluginID)
	if got := state.Settings[resolverSettingDouyinCookie]; got != "keep-me" {
		t.Fatalf("空串提交后凭据应保持不变，实际 %#v", got)
	}
	if state.Settings[resolverSettingMaxImages] != float64(6) {
		t.Fatal("同批提交的普通设置应生效")
	}
}

// 前端只提交「与默认值不同」的键，凭据保持脱敏时整个键都不会出现在 payload 里。
// 这种情况同样必须保留原值，否则改一下别的设置就把 Cookie 弄丢了。
func TestUpdateSettingsKeepsSecretWhenKeyOmitted(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingXHSCookie: "keep-me",
	}); err != nil {
		t.Fatalf("首次保存失败：%v", err)
	}
	// 完全不提 xhs_cookie，只改别的设置。
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingTimeoutSeconds: float64(12),
	}); err != nil {
		t.Fatalf("二次保存失败：%v", err)
	}
	state, _ := manager.Get(resolverPluginID)
	if got := state.Settings[resolverSettingXHSCookie]; got != "keep-me" {
		t.Fatalf("未提交的凭据应保持不变，实际 %#v", got)
	}
}

// 恢复默认（提交空 map）也不该顺手把凭据清掉。
func TestUpdateSettingsKeepsSecretOnResetToDefaults(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingXHSCookie: "keep-me",
	}); err != nil {
		t.Fatalf("首次保存失败：%v", err)
	}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{}); err != nil {
		t.Fatalf("恢复默认失败：%v", err)
	}
	state, _ := manager.Get(resolverPluginID)
	if got := state.Settings[resolverSettingXHSCookie]; got != "keep-me" {
		t.Fatalf("恢复默认不该清除凭据，实际 %#v", got)
	}
}

// 只有显式列进 clear 才真的删掉。
func TestUpdateSettingsClearsSecretExplicitly(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingXHSCookie: "drop-me",
	}); err != nil {
		t.Fatalf("首次保存失败：%v", err)
	}
	if _, err := manager.UpdateSettingsWithClears(resolverPluginID, map[string]any{}, []string{resolverSettingXHSCookie}); err != nil {
		t.Fatalf("清除失败：%v", err)
	}
	state, _ := manager.Get(resolverPluginID)
	if _, exists := state.Settings[resolverSettingXHSCookie]; exists {
		t.Fatal("显式清除后凭据应被删除")
	}
}

func TestUpdateSettingsReplacesSecretWhenProvided(t *testing.T) {
	manager := resolverManager(t)
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingDouyinCookie: "old",
	}); err != nil {
		t.Fatalf("首次保存失败：%v", err)
	}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		resolverSettingDouyinCookie: "new",
	}); err != nil {
		t.Fatalf("二次保存失败：%v", err)
	}
	state, _ := manager.Get(resolverPluginID)
	if got := state.Settings[resolverSettingDouyinCookie]; got != "new" {
		t.Fatalf("提交新值时应覆盖，实际 %#v", got)
	}
}

// 插件设置优先于环境变量，两者都没有时返回空串。
func TestResolverCredentialsPreferSettings(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "from-env")
	t.Setenv("DIANA_BILI_SESSDATA", "env-sessdata")

	plain := context.Background()
	if got := resolverDouyinCookie(plain); got != "from-env" {
		t.Fatalf("未配置设置时应回落环境变量，实际 %q", got)
	}

	ctx := withResolverCredentials(plain, resolverCredentials{DouyinCookie: "from-settings"})
	if got := resolverDouyinCookie(ctx); got != "from-settings" {
		t.Fatalf("插件设置应优先，实际 %q", got)
	}
	// 设置里没填的项仍然回落环境变量。
	if got := bilibiliSessdata(ctx); got != "env-sessdata" {
		t.Fatalf("未填写的项应回落环境变量，实际 %q", got)
	}
}

func TestResolverCredentialsFromSettings(t *testing.T) {
	values := SettingValues{
		resolverSettingDouyinCookie: "  dy  ",
		resolverSettingProxyURL:     "http://127.0.0.1:7890",
	}
	creds := resolverCredentialsFromSettings(values)
	if creds.DouyinCookie != "dy" {
		t.Fatalf("应去掉首尾空白，实际 %q", creds.DouyinCookie)
	}
	if creds.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("代理读取错误：%q", creds.ProxyURL)
	}
	if creds.XHSCookie != "" {
		t.Fatalf("未配置项应为空，实际 %q", creds.XHSCookie)
	}
}
