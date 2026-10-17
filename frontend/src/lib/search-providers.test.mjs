import assert from "node:assert/strict";
import { test } from "node:test";
import { changeSearchProviderType, parseSearchParams, searchProviderFromTemplate, searchProviderKeyHint, searchProviderKeyStatus, searchProviderPreset, searchProviderTemplates } from "./search-providers.ts";

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

test("templates fill known services and browser engines keep their engine name", () => {
  const ids = searchProviderTemplates.map(template => template.id);
  assert.equal(new Set(ids).size, ids.length);
  const google = searchProviderFromTemplate(searchProviderTemplates.find(template => template.id === "browser-google"));
  assert.deepEqual([google.id, google.type, google.tool, google.query_param], ["", "browser", "google", "q"]);
  assert.equal(searchProviderTemplates.some(template => template.provider.tool === "baidu"), false);
  const brave = searchProviderTemplates.find(template => template.id === "brave");
  const first = searchProviderFromTemplate(brave);
  first.url = "https://changed.example";
  assert.equal(searchProviderFromTemplate(brave).url, "https://api.search.brave.com/res/v1/web/search");
});

test("every keyed service reports a missing key, not only Tavily", () => {
  for (const type of ["tavily", "perplexity", "tinyfish", "brave"]) assert.equal(searchProviderKeyStatus({ id: type, name: type, type, url: "" }), "待配置密钥");
  assert.equal(searchProviderKeyStatus({ id: "b", name: "b", type: "brave", url: "", api_key_configured: true }), "已配置密钥");
  assert.equal(searchProviderKeyStatus({ id: "h", name: "h", type: "http", url: "", http_config: { auth_type: "header" } }), "待配置密钥");
  assert.equal(searchProviderKeyStatus({ id: "h", name: "h", type: "http", url: "", http_config: { auth_type: "none" } }), "密钥可选");
  assert.equal(searchProviderKeyStatus({ id: "e", name: "e", type: "exa_mcp", url: "" }), "密钥可选");
  assert.equal(searchProviderKeyStatus({ id: "g", name: "g", type: "browser", url: "" }), "");
});

import { readFileSync } from "node:fs";
import ts from "typescript";
import vm from "node:vm";

test("demo global search is shared, preserves disabled routes and rejects invalid saves atomically", async () => {
  const source = readFileSync(new URL("../demo/demo.ts", import.meta.url), "utf8");
  const declarations = source.slice(source.indexOf("let demoSearchProviders"), source.indexOf("function demoPluginForProfile"));
  const handler = source.slice(source.indexOf("  if (path === '/api/assistant/search-assignment')"), source.indexOf('  if (path === "/api/system/media-cache")'));
  const context = vm.createContext({ URL, Response, json: (value, status = 200) => new Response(JSON.stringify(value), { status }) });
  vm.runInContext(ts.transpileModule(declarations + "\nfunction call(path: string, method = 'GET', body: Record<string, unknown> = {}) {" + handler + "}", { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  const path = "/api/assistant/search-assignment";
  const original = await context.call(path).json();
  assert.ok(original.provider_ids.length);
  const saved = { ...original, provider_ids: ["exa", "engine-google"], disabled: true };
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
