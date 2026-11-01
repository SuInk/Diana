import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import ts from "typescript";

function loadTheme(raw, blocked = false) {
  const source = readFileSync(new URL("./theme.ts", import.meta.url), "utf8");
  const context = vm.createContext({ exports: {}, require: () => ({ reactive: x => x, watchEffect: () => {} }),
    window: { localStorage: { getItem: () => { if (blocked) throw new Error("blocked"); return raw; } }, matchMedia: () => ({ matches: false }) }
  });
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, context);
  return context.exports;
}
test("new, invalid and unavailable preferences default to forest green", () => {
  for (const raw of [null, "{broken", JSON.stringify({ accent: "unknown" })]) {
    assert.equal(loadTheme(raw).theme.accent, "forest");
  }
  assert.equal(loadTheme(null, true).theme.accent, "forest");
  assert.equal(loadTheme(null).accentOptions[0].id, "forest");
});
test("existing color and mode preferences remain unchanged", () => {
  for (const accent of ["diana", "forest", "violet", "ocean"]) {
    const { theme } = loadTheme(JSON.stringify({ accent, mode: "dark", showLoginLinks: false }));
    assert.equal(theme.accent, accent); assert.equal(theme.mode, "dark"); assert.equal(theme.showLoginLinks, false);
  }
});
test("static favicon and pre-hydration accent use the green default", () => {
  assert.match(readFileSync(new URL("../../public/favicon.svg", import.meta.url), "utf8"), /#159f6c/);
  assert.match(readFileSync(new URL("../styles/main.css", import.meta.url), "utf8"), /--accent: #159f6c/);
});
