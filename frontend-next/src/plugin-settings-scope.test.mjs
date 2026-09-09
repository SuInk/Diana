import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { pluginForBot } from "./plugin-settings.ts";

test("plugin settings use global requests while guarding stale UI responses", async () => {
  const source = await readFile(new URL("./views/PluginsView.vue", import.meta.url), "utf8");
  assert.match(source, /updatePluginSettings\(target\.manifest\.id, payload, \[\.\.\.clearSecrets\.value\]\)/);
  assert.match(source, /updatePluginSettings\(repositoryPublishPluginID, publishPayload, publishClears\)/);
  assert.match(source, /if \(scope !== botScope\.value\) return;/);
  assert.doesNotMatch(source, /跟随全局默认|全局默认开关|inheritPluginSettings|settings_inherited/);
  assert.match(source, /所有机器人共用此插件配置/);
  assert.match(source, /await listPlugins\(\)/);
  assert.doesNotMatch(source, /listPlugins\(scope\)/);
  assert.doesNotMatch(source, /共享插件设置/);
});

test("OpenAPI configuration lives in system settings", async () => {
  const source = await readFile(new URL("./views/SettingsView.vue", import.meta.url), "utf8");
  assert.match(source, /@click="saveOpenAPISettings"/);
  assert.match(source, /PluginSettingField/);
  assert.doesNotMatch(source, /限流等参数在「插件」页调整/);
});

test("subscription settings list every robot and only use the selected robot as a new-target default", async () => {
  for (const view of ["RepositoryWatchManager", "RSSWatchManager"]) {
    const source = await readFile(new URL(`./components/${view}.vue`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /task\.profile_id === props\./);
    assert.match(source, /SubscriptionTargetsEditor/);
    assert.doesNotMatch(source, /target\.profile_id === props\./);
    assert.match(source, /props\.defaultProfileId/);
  }
});


test("switching robots changes enabled state without replacing shared settings or secret flags", () => {
  const state = { manifest: { id: "official.rss-watch" }, enabled: true, profile_enabled: { qq: false, tg: true }, settings: { timeout_seconds: 45 }, secrets_configured: { token: true } };
  const qq = pluginForBot(state, "qq"), tg = pluginForBot(state, "tg");
  assert.equal(qq.enabled, false);
  assert.equal(tg.enabled, true);
  assert.strictEqual(qq.settings, tg.settings);
  assert.strictEqual(qq.secrets_configured, tg.secrets_configured);
  assert.equal(state.enabled, true);
  assert.strictEqual(pluginForBot(state, ""), state);
});
