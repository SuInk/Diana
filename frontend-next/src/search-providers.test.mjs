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
