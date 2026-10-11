import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";
import { computed, ref, watch } from "vue";
import { createResolverCredentialChecks, groupResolverSettings } from "./resolver-credentials.ts";

const source = readFileSync(new URL("./views/PluginsView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
function runFunction(name, context, sourceAST = ast) {
  const declaration = sourceAST.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name);
  assert.ok(declaration, `missing ${name}`);
  const text = declaration.getText(sourceAST).replace(/^export /, "");
  vm.runInContext(ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
}
const key = "bili_sessdata";
function editor() {
  const plugin = { manifest: { id: "official.nonebot-plugin-resolver-go", name: "链接解析", settings: [{ key, type: "string", secret: true, default: "" }, { key: "enabled_platforms", type: "multi_select", default: ["bilibili"] }] }, settings: { [key]: "" }, secrets_configured: { [key]: true } };
  const checks = [];
  const saves = [];
  const notices = [];
  const target = ref(null), form = ref({}), clears = ref([]);
  const context = vm.createContext({
    Error, Element: class Element { constructor(selector) { this.selector = selector; } closest(selectors) { return selectors.split(", ").includes(this.selector); } },
    document: new EventTarget(), window: new EventTarget(), settingsPointerTarget: null,
    settingsTarget: target, settingsForm: form, clearSecrets: clears,
    settingsSpecs: computed(() => target.value?.manifest.settings ?? []),
    repositoryPublishSpecs: ref([]), repositoryPublishTarget: ref(null), repositoryPublishForm: ref({}),
    isGitHubSettings: ref(false), repositoryWatchRef: ref(null), rssWatchRef: ref(null), credentialEditor: ref(null),
    credentialTokenDrafts: ref({}), savingSettings: ref(false), savingSubscription: ref(false),
    openedSnapshot: ref(""), musicTestResults: ref({}), githubSettingsTab: ref(""), stickerSettingsTab: ref(""),
    plugins: ref([plugin]), repositoryPublishPluginID: "publish", repositoryWatchPluginID: "watch",
    botScope: ref("synthetic-bot"), viewQuery: () => new URLSearchParams(), navigate() {},
    askConfirm: async () => true, upsert() {},
    toastSuccess: (message) => notices.push(["success", message]), toastError: (message) => notices.push(["error", message]),
    updatePluginSettings: (id, settings, clearSecrets) => new Promise((resolve, reject) => saves.push({ id, settings, clearSecrets, resolve, reject })),
  });
  context.isResolverSettings = computed(() => target.value?.manifest.id === "official.nonebot-plugin-resolver-go");
  context.resolverGroups = computed(() => groupResolverSettings(context.settingsSpecs.value));
  context.activeSettingsForm = computed(() => form.value);
  context.settingsBusy = computed(() => context.savingSettings.value || context.savingSubscription.value);
  for (const name of ["secretConfigured", "resolverInput"]) runFunction(name, context);
  context.resolverChecks = createResolverCredentialChecks({ input: context.resolverInput, request: (key, signal) => new Promise((resolve, reject) => checks.push({ key, signal, resolve, reject })) });
  for (const name of ["settingsSnapshot", "settingsDirty", "openSettings", "toggleClearSecret", "discardSettings", "closeSettings", "resetSettings", "buildSettingsPayload", "persistSettings", "saveSettings", "clearSettingsPointerTarget", "onSettingsPointerDown", "onResolverBlur"]) runFunction(name, context);
  context.openSettings(plugin);
  return { context, checks, saves, notices, plugin };
}
const tick = () => Promise.resolve();
const result = { key, state: "valid", configured: true, label: "B站", message: "synthetic success" };

test("each real opening checks configured cookies once, even with platforms disabled, but skips empty credentials and files", async () => {
  const { context: c, checks, plugin: initial } = editor();
  c.discardSettings();
  const plugin = {
    ...initial,
    manifest: { ...initial.manifest, settings: [
      ...initial.manifest.settings,
      { key: "douyin_cookie", type: "string", secret: true, default: "" },
      { key: "xhs_cookie", type: "string", secret: true, default: "" },
      { key: "ytdlp_cookies_path", type: "string", default: "" },
    ] },
    settings: { ...initial.settings, enabled_platforms: [], ytdlp_cookies_path: "synthetic-cookies.txt" },
    secrets_configured: { ...initial.secrets_configured, douyin_cookie: true },
  };
  c.openSettings(plugin);
  const first = c.resolverChecks.test(key);
  await tick();
  assert.deepEqual(checks.map((check) => check.key), [key, "douyin_cookie"]);
  assert.equal(c.settingsForm.value[key], "");
  assert.equal(c.settingsDirty(), false);
  checks[0].resolve(result);
  checks[1].resolve({ ...result, key: "douyin_cookie" });
  await first;
  c.onResolverBlur(key, { relatedTarget: null });
  await tick();
  assert.equal(checks.length, 2);
  c.discardSettings();
  c.openSettings(plugin);
  await tick();
  assert.deepEqual(checks.map((check) => check.key), [key, "douyin_cookie", key, "douyin_cookie"]);
  c.discardSettings();
  assert.equal(checks[2].signal.aborted, true);
  assert.equal(checks[3].signal.aborted, true);
  c.openSettings({ ...plugin, secrets_configured: {} });
  await tick();
  assert.equal(checks.length, 4);
  c.openSettings({ ...plugin, manifest: { ...plugin.manifest, id: "other-plugin" } });
  await tick();
  assert.equal(checks.length, 4);
  c.resolverChecks.dispose();
});

test("clearing another credential preserves both pending checks and completed results", async () => {
  const { context: c, checks } = editor();
  const pending = c.resolverChecks.test(key);
  await tick();
  c.toggleClearSecret("xhs_cookie");
  assert.equal(checks[0].signal.aborted, false);
  assert.equal(c.resolverChecks.states[key].phase, "checking");
  assert.equal(c.resolverChecks.test(key), pending);
  checks[0].resolve(result);
  await pending;
  c.toggleClearSecret("xhs_cookie");
  assert.equal(c.resolverChecks.states[key].phase, "done");
  assert.equal(c.resolverChecks.states[key].check.state, "valid");
  c.settingsForm.value.enabled_platforms = [];
  assert.equal(c.resolverChecks.states[key].phase, "done");
  c.toggleClearSecret(key);
  assert.equal(c.resolverChecks.states[key].phase, "pending");
  c.resolverChecks.dispose();
});

test("controller resume after a cancelled close does not start another automatic round", async () => {
  const { context: c, checks } = editor();
  await tick();
  assert.equal(checks.length, 1);
  c.settingsForm.value.enabled_platforms = [];
  c.askConfirm = async () => false;
  await c.closeSettings();
  await tick();
  assert.equal(checks[0].signal.aborted, true);
  assert.equal(checks.length, 1);
  c.resolverChecks.dispose();
});

test("cancelled close and reset preserve completed checks and untouched draft blur eligibility", async () => {
  for (const action of ["closeSettings", "resetSettings"]) {
    const { context: c, checks } = editor();
    const completed = c.resolverChecks.test(key);
    await tick();
    checks[0].resolve(result);
    await completed;
    c.settingsForm.value.xhs_cookie = "synthetic-unchecked-draft";
    c.askConfirm = async () => false;
    await c[action]();
    assert.equal(c.resolverChecks.states[key].phase, "done");
    assert.equal(c.resolverChecks.states[key].check.state, "valid");
    assert.equal(c.settingsForm.value.xhs_cookie, "synthetic-unchecked-draft");
    c.onResolverBlur("xhs_cookie", { relatedTarget: null });
    const pending = c.resolverChecks.test("xhs_cookie");
    await tick();
    assert.deepEqual(checks.map(check => check.key), [key, "xhs_cookie"]);
    checks[1].resolve({ ...result, key: "xhs_cookie" });
    await pending;
    c.resolverChecks.dispose();
  }
});

test("cancelled confirmations abort in-flight probes and allow one resumed draft blur", async () => {
  for (const action of ["closeSettings", "resetSettings"]) {
    const { context: c, checks } = editor();
    c.settingsForm.value[key] = "synthetic-draft";
    const old = c.resolverChecks.test(key, "blur");
    await tick();
    let answer;
    c.askConfirm = () => new Promise(resolve => { answer = resolve; });
    const confirming = c[action]();
    assert.equal(checks[0].signal.aborted, true);
    await c.resolverChecks.test(key, "blur");
    assert.equal(checks.length, 1);
    answer(false);
    await confirming;
    const resumed = c.resolverChecks.test(key, "blur");
    await tick();
    assert.equal(checks.length, 2);
    checks[0].resolve({ ...result, state: "invalid" });
    await old;
    assert.equal(c.resolverChecks.states[key].phase, "checking");
    checks[1].resolve(result);
    await resumed;
    await c.resolverChecks.test(key, "blur");
    assert.equal(checks.length, 2);
    c.resolverChecks.dispose();
  }
});

test("actual settings handlers keep failed save draft and close/abort only after successful save", async () => {
  const { context: c, checks, saves, notices } = editor();
  c.settingsForm.value[key] = "synthetic-draft";
  const checking = c.resolverChecks.test(key);
  await tick();
  const failedSave = c.saveSettings();
  assert.equal(saves.length, 1);
  assert.equal(saves[0].settings[key], "synthetic-draft");
  saves[0].reject(new Error("synthetic save error"));
  await failedSave;
  assert.equal(c.savingSettings.value, false);
  assert.ok(c.settingsTarget.value);
  assert.equal(c.settingsForm.value[key], "synthetic-draft");
  assert.equal(notices[0][0], "error");
  const saved = c.saveSettings();
  saves[1].resolve({ ...c.settingsTarget.value, settings: {} });
  await saved;
  assert.equal(c.settingsTarget.value, null);
  assert.equal(checks[0].signal.aborted, true);
  checks[0].resolve(result);
  await checking;
  assert.equal(c.resolverChecks.states[key].check, undefined);
  c.resolverChecks.dispose();
});

test("cancel aborts before confirmation, a rejected discard can retry, and reopening rejects old responses", async () => {
  const { context: c, checks, plugin } = editor();
  c.settingsForm.value[key] = "synthetic-draft";
  const old = c.resolverChecks.test(key);
  await tick();
  let answer;
  c.askConfirm = () => new Promise((resolve) => { answer = resolve; });
  const close = c.closeSettings();
  assert.equal(checks[0].signal.aborted, true);
  answer(false);
  await close;
  assert.ok(c.settingsTarget.value);
  assert.equal(c.settingsForm.value[key], "synthetic-draft");
  const retry = c.resolverChecks.test(key);
  await tick();
  checks[0].resolve(result);
  await old;
  assert.equal(c.resolverChecks.states[key].phase, "checking");
  c.discardSettings();
  c.openSettings(plugin);
  checks[1].resolve(result);
  await retry;
  assert.equal(c.resolverChecks.states[key].phase, "checking");
  assert.equal(c.resolverChecks.states[key].check, undefined);
  assert.equal(checks.length, 3);
  c.resolverChecks.dispose();
});

test("actual clear, undo and defaults retain blank-reuse and explicit-clear payload semantics", async () => {
  const { context: c, checks, saves } = editor();
  c.settingsForm.value[key] = "synthetic-draft";
  const pending = c.resolverChecks.test(key);
  await tick();
  c.toggleClearSecret(key);
  assert.equal(checks[0].signal.aborted, true);
  assert.equal(c.settingsForm.value[key], "");
  assert.ok(c.clearSecrets.value.includes(key));
  await c.resolverChecks.test(key);
  assert.equal(checks.length, 1);
  c.toggleClearSecret(key);
  assert.equal(c.clearSecrets.value.length, 0);
  assert.equal(c.buildSettingsPayload()[key], undefined);
  await c.resetSettings();
  assert.ok(c.clearSecrets.value.includes(key));
  const save = c.saveSettings();
  assert.equal(saves[0].settings[key], undefined);
  assert.ok(saves[0].clearSecrets.includes(key));
  saves[0].reject(new Error("synthetic failure"));
  await save;
  checks[0].resolve(result);
  await pending;
  assert.equal(c.resolverChecks.states[key].check, undefined);
  c.resolverChecks.dispose();
});

test("blur does not test before destructive or manual actions, but ordinary blur tests once", async () => {
  const { context: c, checks } = editor();
  c.settingsForm.value[key] = "synthetic-draft";
  for (const selector of ["[data-secret-clear]", "[data-settings-action]", ".modal-header button", `[data-resolver-test="${key}"]`]) {
    c.onResolverBlur(key, { relatedTarget: new c.Element(selector) });
  }
  await tick();
  assert.equal(checks.length, 0);
  c.onResolverBlur(key, { relatedTarget: null });
  const request = c.resolverChecks.test(key);
  await tick();
  assert.equal(checks.length, 1);
  checks[0].resolve(result);
  await request;
  c.resolverChecks.dispose();
});

test("pointer capture suppresses Safari null-target blur only until pointer release or cancellation", async () => {
  assert.match(source, /@pointerdown="onSettingsPointerDown"/);
  const modal = readFileSync(new URL("./components/Modal.vue", import.meta.url), "utf8");
  assert.match(modal, /@pointerdown\.capture="emit\('pointerdown', \$event\)"/);
  for (const end of ["pointerup", "pointercancel", "window-blur"]) {
    for (const selector of ["[data-secret-clear]", "[data-settings-action]", ".modal-header button", `[data-resolver-test="${key}"]`]) {
      const { context: c, checks } = editor();
      c.settingsForm.value[key] = "synthetic-draft";
      c.onSettingsPointerDown({ target: new c.Element(selector) });
      c.onResolverBlur(key, { relatedTarget: null });
      await tick();
      assert.equal(checks.length, 0, `${selector}: blur before click dispatched a request`);
      if (end === "window-blur") c.window.dispatchEvent(new Event("blur"));
      else c.document.dispatchEvent(new Event(end));
      assert.equal(c.settingsPointerTarget, null);
      c.onResolverBlur(key, { relatedTarget: null });
      const pending = c.resolverChecks.test(key);
      await tick();
      assert.equal(checks.length, 1, `${end}: stale target suppressed ordinary blur`);
      checks[0].resolve(result);
      await pending;
      c.resolverChecks.dispose();
    }
  }
});

test("actual scope watcher and KeepAlive/unmount hooks synchronously abort pending checks", async () => {
  for (const action of ["scope", "deactivate", "unmount"]) {
    const { context: c, checks } = editor();
    const callbacks = {};
    const stops = [];
    Object.assign(c, {
      watch: (...args) => { const stop = watch(...args); stops.push(stop); return stop; },
      onDeactivated: (callback) => { callbacks.deactivate = callback; },
      onBeforeUnmount: (callback) => { callbacks.unmount = callback; },
      permissionsTarget: ref(null), dependenciesTarget: ref(null), exceptionsTarget: ref(null), reload: async () => {},
    });
    for (const statement of ast.statements) {
      const text = statement.getText(ast);
      if (text.startsWith("watch(botScope,") || text.startsWith("onDeactivated(") || text.startsWith("onBeforeUnmount(")) {
        vm.runInContext(ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, c);
      }
    }
    c.settingsForm.value[key] = "synthetic-draft";
    const pending = c.resolverChecks.test(key);
    await tick();
    if (action === "scope") {
      c.botScope.value = "synthetic-other";
      assert.equal(c.settingsTarget.value, null);
    } else callbacks[action]();
    assert.equal(checks[0].signal.aborted, true);
    checks[0].resolve(result);
    await pending;
    assert.equal(c.resolverChecks.states[key].check, undefined);
    stops.forEach((stop) => stop());
    c.resolverChecks.dispose();
  }
});

test("resolver API preserves all-item compatibility, encodes selection and forwards AbortSignal", async () => {
  const api = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
  const apiAST = ts.createSourceFile("api.ts", api, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const calls = [];
  const context = vm.createContext({ requestJSON: async (url, init) => { calls.push({ url, init }); return { credentials: [] }; } });
  runFunction("testResolverCredentials", context, apiAST);
  await context.testResolverCredentials({}, []);
  assert.equal(calls[0].url, "/api/assistant/plugins/resolver/test?profile=");
  const controller = new AbortController();
  await context.testResolverCredentials({ [key]: "synthetic-draft" }, ["douyin_cookie"], "synthetic bot", { credentialKey: key, signal: controller.signal });
  assert.equal(calls[1].url, `/api/assistant/plugins/resolver/test?profile=synthetic%20bot&credential_key=${key}`);
  assert.equal(calls[1].init.signal, controller.signal);
  assert.deepEqual(JSON.parse(calls[1].init.body), { settings: { [key]: "synthetic-draft" }, clear_secrets: ["douyin_cookie"] });
});
