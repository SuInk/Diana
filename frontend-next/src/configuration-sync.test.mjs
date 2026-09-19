import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { effectScope, nextTick } from "vue";
import { configurationKindForMutation, notifyConfigurationChanged, useConfigurationRefresh } from "./configuration-sync.ts";

const flush = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)); };

function apiHarness(fetch) {
  const changes = [];
  const source = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
  const context = vm.createContext({ exports: {}, fetch, console, AbortController, window: { dispatchEvent() {} }, require: name => {
    if (name === "./scope-transition") return { trackScopeRequest: () => () => {} };
    if (name === "./configuration-sync") return { configurationKindForMutation, notifyConfigurationChanged: kind => changes.push(kind) };
    throw new Error(`unexpected import ${name}`);
  }});
  vm.runInContext(compiled, context);
  return { api: context.exports, changes };
}
const response = (value, status = 200) => new Response(JSON.stringify(value), { status });

test("saved bot and model mutations notify other pages; failed saves and model tests do not", async () => {
  const { api, changes } = apiHarness(async (url) => response({}, url.endsWith("/delete") ? 400 : 200));
  await api.saveBotProfileConfig({ enabled: true });
  await api.createBotProfileConfig({ enabled: true });
  await api.saveConfig({ model: "new-model" });
  await api.importConfigProfiles({ profiles: [] });
  await api.testLLM("hello");
  await assert.rejects(api.deleteConfigProfile("provider"));
  assert.deepEqual(changes, ["bot", "bot", "llm", "llm"]);
});

test("a pre-save read cannot overwrite the post-save cache or be reused as a new read", async () => {
  let finishOld;
  let reads = 0;
  const { api } = apiHarness(async (_url, init) => {
    if (init.method === "POST") return response({ model: "new" });
    reads++;
    if (reads === 1) return new Promise(resolve => { finishOld = resolve; });
    return response({ model: "new" });
  });
  const oldRead = api.getConfig();
  await api.saveConfig({ model: "new" });
  const freshRead = await api.getConfig();
  assert.equal(freshRead.model, "new");
  finishOld(response({ model: "old" }));
  assert.equal((await oldRead).model, "new");
  assert.equal((await api.getConfig()).model, "new");
  assert.equal(reads, 2);
});

test("cached page subscriptions refresh after changes, serialize updates, and stop on disposal", async () => {
  const scope = effectScope();
  let calls = 0;
  let finish;
  scope.run(() => useConfigurationRefresh(["llm"], async () => {
    calls++;
    if (calls === 1) await new Promise(resolve => { finish = resolve; });
  }));
  notifyConfigurationChanged("bot");
  await flush();
  assert.equal(calls, 0);
  notifyConfigurationChanged("llm");
  await flush();
  assert.equal(calls, 1);
  notifyConfigurationChanged("llm");
  await flush();
  assert.equal(calls, 1);
  finish();
  await flush();
  assert.equal(calls, 2);
  scope.stop();
  notifyConfigurationChanged("llm");
  await flush();
  assert.equal(calls, 2);
});
