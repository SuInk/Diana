import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import * as vue from "vue";

const app = readFileSync(new URL("../App.vue", import.meta.url), "utf8");
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const flush = async () => { await vue.nextTick(); await new Promise(r => setImmediate(r)); };
function appHarness(overrides = {}) {
  const calls = [];
  const listeners = {};
  const currentView = vue.ref("dashboard");
  const botScope = vue.ref("");
  let mounted;
  const api = {
    getAuthStatus: async () => ({ authenticated: true }),
    getBotProfileConfig: async () => ({ profiles: [{ id: "bot-a" }] }),
    getHealth: async () => ({}), getSystemVersion: async () => ({ version_label: "v1" }),
    getConfig: async () => ({ api_key_configured: true }), checkForUpdate: async () => ({}),
    ...overrides
  };
  const router = { currentView, navigate: view => { currentView.value = view; }, navItems: [], navSections: () => [], navItemForView: v => v };
  const scope = vue.effectScope();
  const context = vm.createContext({ exports: {}, console, window: {
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    localStorage: { getItem: () => null, setItem() {} }, setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1, clearInterval() {},
    addEventListener: (name, fn) => { listeners[name] = fn; }, location: { reload() { calls.push("reload"); } }
  }, require: name => {
    if (name === "vue") return { ...vue, onMounted: fn => { mounted = fn; }, onBeforeUnmount() {} };
    if (name === "./api") return api;
    if (name === "./router") return router;
    if (name === "./lib/bot-scope") return { ALL_PROFILES: "", botScope, reconcileBotScope() {}, setBotScope: id => { botScope.value = id; } };
    if (name === "./lib/space-unread") return { refreshSpaceUnread: async () => { calls.push("unread"); }, markSpaceSeen() {} };
    if (name === "./lib/stream") return { stream: vue.reactive({ connected: false }), startEventStream: () => calls.push("stream") };
    if (name === "./lib/configuration-sync") return { useConfigurationRefresh() {} };
    if (name === "./lib/theme") return { theme: { mode: "auto" } };
    if (name === "./lib/backendState") return { updateInstallingRecently: () => false, clearUpdateInstalling() {}, autoReloadAllowed: () => false };
    if (name === "./lib/toast") return { toastSuccess: text => calls.push(text) };
    return {};
  }});
  const script = app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1].replaceAll('import.meta.env.VITE_DEMO_MODE', '"false"') +
    '\nexport const harness = { booting, locked, scopeReady, backendDown, botScope, systemVersion, retryBackend, showBackendDown };';
  scope.run(() => vm.runInContext(ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context));
  return { ...context.exports.harness, calls, listeners, currentView, mount: () => mounted(), stop: () => scope.stop() };
}

test("auth gates all business calls; only profiles gate content, not background reads", async () => {
  const auth = deferred(), profiles = deferred(), background = deferred();
  const h = appHarness({ getAuthStatus: () => auth.promise, getBotProfileConfig: () => profiles.promise,
    getHealth: () => background.promise, getSystemVersion: () => background.promise, getConfig: () => background.promise });
  try {
    const boot = h.mount();
    assert.deepEqual(h.calls, []);
    assert.equal(h.booting.value, true);
    auth.resolve({ authenticated: true }); await boot; await flush();
    assert.equal(h.booting.value, false);
    assert.equal(h.scopeReady.value, false);
    assert.deepEqual(h.calls, ["stream"]);
    profiles.resolve({ profiles: [{ id: "bot-a" }] }); await flush();
    assert.equal(h.scopeReady.value, true);
    assert.equal(h.botScope.value, "bot-a");
    assert.ok(h.calls.includes("unread"));
  } finally { h.stop(); }
});

test("late config does not steal navigation after leaving and returning", async () => {
  const config = deferred();
  const h = appHarness({ getConfig: () => config.promise });
  try {
    await h.mount(); await flush();
    h.currentView.value = "settings"; h.currentView.value = "dashboard";
    config.resolve({ api_key_configured: false }); await flush();
    assert.equal(h.currentView.value, "dashboard");
  } finally { h.stop(); }
});

test("401 invalidates pending startup profile/config/version responses", async () => {
  const late = deferred();
  const h = appHarness({ getBotProfileConfig: () => late.promise, getConfig: () => late.promise, getSystemVersion: () => late.promise });
  try {
    await h.mount();
    h.listeners["diana:unauthorized"]();
    late.resolve({ profiles: [{ id: "old" }], api_key_configured: false, version_label: "old" }); await flush();
    assert.equal(h.locked.value, true);
    assert.equal(h.scopeReady.value, false);
    assert.equal(h.botScope.value, "");
    assert.equal(h.systemVersion.value, null);
    assert.equal(h.currentView.value, "dashboard");
    assert.ok(!h.calls.includes("unread"));
  } finally { h.stop(); }
});

test("recovery compares fresh version response, not the stale version ref", async () => {
  let version = "v1";
  const h = appHarness({ getSystemVersion: async () => ({ version_label: version }) });
  try {
    await h.mount(); await flush();
    h.showBackendDown("offline"); version = "v2";
    await h.retryBackend(); await flush();
    assert.ok(h.calls.some(call => call.includes("后端已更新到 v2")));
  } finally { h.stop(); }
});

test("HTML and Vue both provide accessible loading feedback and content waits for scope", () => {
  const html = readFileSync(new URL("../../index.html", import.meta.url), "utf8");
  assert.match(html, /role="status"/);
  assert.match(html, /prefers-reduced-motion/);
  assert.match(app, /v-if="booting" class="startup-loading"/);
  assert.match(app, /v-if="!scopeReady"[\s\S]*?<div v-else[\s\S]*?<KeepAlive/);
});

function apiHarness(fetch) {
  const timers = new Map(); let id = 0;
  const source = readFileSync(new URL("../api.ts", import.meta.url), "utf8");
  const context = vm.createContext({ exports: {}, fetch, AbortController, window: {
    setTimeout: fn => { timers.set(++id, fn); return id; }, clearTimeout: id => timers.delete(id), dispatchEvent() {}
  }, require: () => ({ trackScopeRequest: () => () => {}, describeServerFailure: () => "server" }) });
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return { api: context.exports, timers, expire: () => { for (const fn of [...timers.values()]) fn(); } };
}
for (const phase of ["fetch", "body"]) {
  test(`startup timeout covers ${phase}, cleans timer, and retry starts a new request`, async () => {
    let calls = 0;
    const h = apiHarness(async (_url, init) => {
      calls++;
      if (calls > 1) return new Response(JSON.stringify({ authenticated: true }));
      const blocked = () => new Promise((_, reject) => init.signal.addEventListener("abort", () => reject(new Error("aborted"))));
      return phase === "fetch" ? blocked() : { text: blocked };
    });
    const first = h.api.getAuthStatus(); await flush(); h.expire();
    await assert.rejects(first, error => error.kind === "offline" && /超时/.test(error.message));
    assert.equal(h.timers.size, 0);
    assert.equal((await h.api.getAuthStatus()).authenticated, true);
    assert.equal(calls, 2);
    assert.equal(h.timers.size, 0);
  });
}

test("only explicit startup profile reads have a timeout and bypass a hung shared read", async () => {
  let calls = 0;
  const h = apiHarness(async (_url, init) => { calls++; return init.signal ? new Response('{"id":"fresh"}') : new Promise(() => {}); });
  void h.api.getBotProfileConfig();
  assert.equal(h.timers.size, 0);
  assert.equal((await h.api.getBotProfileConfig(false, 10000)).id, "fresh");
  assert.equal(calls, 2);
  assert.equal(h.timers.size, 0);
});
