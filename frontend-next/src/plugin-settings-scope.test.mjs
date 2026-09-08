import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("plugin settings preserve the request scope across asynchronous saves", async () => {
  const source = await readFile(new URL("./views/PluginsView.vue", import.meta.url), "utf8");
  assert.match(source, /updatePluginSettings\(target\.manifest\.id, payload, \[\.\.\.clearSecrets\.value\], scope\)/);
  assert.match(source, /updatePluginSettings\(repositoryPublishPluginID, publishPayload, publishClears, scope\)/);
  assert.match(source, /if \(scope !== botScope\.value\) return;/);
  assert.doesNotMatch(source, /跟随全局默认|全局默认开关|inheritPluginSettings|settings_inherited/);
  assert.match(source, /if \(!scope\)/);
  assert.doesNotMatch(source, /共享插件设置/);
});

test("OpenAPI configuration lives in system settings", async () => {
  const source = await readFile(new URL("./views/SettingsView.vue", import.meta.url), "utf8");
  assert.match(source, /@click="saveOpenAPISettings"/);
  assert.match(source, /PluginSettingField/);
  assert.doesNotMatch(source, /限流等参数在「插件」页调整/);
});

test("subscription editors use their parent robot profile", async () => {
  for (const view of ["RepositoryWatchManager", "RSSWatchManager"]) {
    const source = await readFile(new URL(`./components/${view}.vue`, import.meta.url), "utf8");
    assert.match(source, /task\.profile_id === props\.profileId/);
    assert.match(source, /profile\.id === props\.profileId/);
  }
});
