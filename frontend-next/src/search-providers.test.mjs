import assert from "node:assert/strict";
import { test } from "node:test";
import { changeSearchProviderType, parseSearchParams, searchProviderKeyHint, searchProviderPreset } from "./search-providers.ts";

test("switching protocols replaces preset addresses but preserves custom endpoints", () => {
  const exa = { id: "exa", name: "Exa", type: "exa_mcp", ...searchProviderPreset("exa_mcp") };
  assert.equal(changeSearchProviderType(exa, "tavily").url, "https://api.tavily.com/search");
  const custom = { ...exa, url: "https://private.example/mcp" };
  assert.equal(changeSearchProviderType(custom, "http").url, custom.url);
  const http = changeSearchProviderType(exa, "http");
  http.http_config.results_path = "data.items";
  assert.equal(changeSearchProviderType(http, "http").http_config.results_path, "data.items");
  assert.equal(searchProviderPreset("http").http_config.results_path, undefined);
  assert.equal(changeSearchProviderType(http, "browser").http_config, undefined);
});

test("fixed search params accept objects and reject other JSON shapes", () => {
  assert.deepEqual(parseSearchParams(' {"language":"zh","nested":{"token":"{api_key}"}} '), { language: "zh", nested: { token: "{api_key}" } });
  assert.deepEqual(parseSearchParams(""), {});
  for (const value of ["[]", "null", '"token"', "false", "{bad}"]) assert.throws(() => parseSearchParams(value), /JSON/);
});

test("key-free Exa hint is restricted to the public host", () => {
  const provider = { id: "exa", name: "Exa", type: "exa_mcp", url: "https://mcp.exa.ai/mcp" };
  assert.match(searchProviderKeyHint(provider), /无需 API Key/);
  assert.doesNotMatch(searchProviderKeyHint({ ...provider, url: "https://mcp.exa.ai.example.org/mcp" }), /无需 API Key/);
  assert.match(searchProviderKeyHint({ ...provider, type: "tavily" }), /需要 Tavily API Key/);
});

import { readFileSync } from "node:fs";
import ts from "typescript";
import { ref, computed, watch, nextTick, effectScope } from "vue";

function globalSearchHarness(save) {
  const source = readFileSync(new URL("./views/LLMView.vue", import.meta.url), "utf8");
  const block = source.slice(source.indexOf("const searchAssignment ="), source.indexOf("interface LLMFormState"));
  const scope = effectScope();
  let refresh;
  let reads = 0;
  const state = scope.run(() => new Function("ref", "computed", "watch", "providerTab", "getSearchAssignment", "saveSearchAssignment", "toastSuccess", "toastError", "useConfigurationRefresh", ts.transpileModule(block, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText + "\nreturn {searchAssignment, searchAssignmentDirty, searchAssignmentError, providerTab, loadSearchAssignment, saveGlobalSearch};")(
    ref, computed, watch, ref("model"), async () => { reads++; return { provider_ids: ["browser"], disabled: false }; }, save, () => {}, () => {}, (_, fn) => { refresh = fn; }
  ));
  return { state, scope, refresh: () => refresh(), reads: () => reads };
}
const flushSearch = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)); };

test("global search draft survives tab switches, refresh and failed saves", async () => {
  const harness = globalSearchHarness(async () => { throw new Error("save rejected"); });
  const { state, scope } = harness;
  state.providerTab.value = "search";
  await flushSearch();
  state.searchAssignment.value.provider_ids = ["exa", "browser"];
  state.searchAssignment.value.disabled = true;
  state.providerTab.value = "model";
  await flushSearch();
  state.providerTab.value = "search";
  await flushSearch();
  await harness.refresh();
  assert.equal(harness.reads(), 1);
  await state.saveGlobalSearch();
  assert.deepEqual(state.searchAssignment.value.provider_ids, ["exa", "browser"]);
  assert.equal(state.searchAssignment.value.disabled, true);
  assert.equal(state.searchAssignmentDirty.value, true);
  assert.match(state.searchAssignmentError.value, /save rejected/);
  scope.stop();
});

test("global search save preserves edits made in-flight and rejects enabled empty routes", async () => {
  let finish;
  let submitted;
  const { state, scope } = globalSearchHarness(value => { submitted = value; return new Promise(resolve => { finish = resolve; }); });
  await state.loadSearchAssignment();
  state.searchAssignment.value.provider_ids = [];
  await state.saveGlobalSearch();
  assert.equal(submitted, undefined);
  assert.ok(state.searchAssignmentError.value);
  state.searchAssignment.value.disabled = true;
  const pending = state.saveGlobalSearch();
  state.searchAssignment.value.provider_ids = ["exa"];
  finish(submitted);
  await pending;
  assert.deepEqual(state.searchAssignment.value.provider_ids, ["exa"]);
  assert.equal(state.searchAssignmentDirty.value, true);
  const final = state.saveGlobalSearch();
  finish(submitted);
  await final;
  assert.equal(state.searchAssignmentDirty.value, false);
  scope.stop();
});

import vm from "node:vm";

test("demo global search is shared, preserves disabled routes and rejects invalid saves atomically", async () => {
  const source = readFileSync(new URL("./demo.ts", import.meta.url), "utf8");
  const declarations = source.slice(source.indexOf("let demoSearchProviders"), source.indexOf("function demoPluginForProfile"));
  const handler = source.slice(source.indexOf("  if (path === '/api/assistant/search-assignment')"), source.indexOf('  if (path === "/api/system/media-cache")'));
  const context = vm.createContext({ URL, Response, json: (value, status = 200) => new Response(JSON.stringify(value), { status }) });
  vm.runInContext(ts.transpileModule(declarations + "\nfunction call(path: string, method = 'GET', body: Record<string, unknown> = {}) {" + handler + "}", { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  const path = "/api/assistant/search-assignment";
  const original = await context.call(path).json();
  assert.ok(original.provider_ids.length);
  const saved = { ...original, provider_ids: ["exa", "browser"], disabled: true };
  assert.deepEqual(await context.call(path, "PUT", saved).json(), saved);
  assert.deepEqual(await context.call(path).json(), saved);
  assert.equal(context.call(path, "PUT", { ...saved, disabled: false, provider_ids: [] }).status, 400);
  assert.equal(context.call(path, "PUT", { ...saved, max_results: 99 }).status, 400);
  assert.deepEqual(await context.call(path).json(), saved);
  assert.equal(context.call("/api/assistant/search-providers/exa", "DELETE").status, 409);
  assert.deepEqual((await context.call("/api/assistant/search-providers").json()).default_assignment, saved);
  const empty = { ...saved, provider_ids: [] };
  assert.deepEqual(await context.call(path, "PUT", empty).json(), empty);
  assert.deepEqual((await context.call(path).json()).provider_ids, []);
  assert.equal(context.call("/api/assistant/search-providers/exa", "DELETE").status, 200);
});
