import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
function loadFunction(name, context) {
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
  assert.ok(fn, `missing ${name}`);
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return context[name];
}

test("saving requires an explicit provider and model for each role", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  for (const key of keys) {
    for (const invalid of [undefined, { model: "m" }, { profile_id: "p", model: "" }]) {
      const errors = [];
      const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
      roles[key] = invalid;
      const busy = { value: false };
      const context = vm.createContext({ form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } }, roleForm: { value: roles }, modelRoleRows: keys.map(key => ({ key, label: key })), editorTab: { value: "access" }, validWebSocketURL: () => true, roleModelIsSelectable: () => true, toastError: message => errors.push(message), busy });
      await loadFunction("save", context)();
      assert.equal(busy.value, false);
      assert.equal(errors.length, 1);
      assert.ok(errors[0].startsWith(key));
    }
  }
});

test("provider and model menus do not offer an empty assignment", () => {
  const context = vm.createContext({ llmChannels: { value: [] }, channelGroups: () => [], selectedRoleProfiles: () => [], modelsForRole: () => [], modelRoleRows: [], GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::" });
  loadFunction("crossProviderModelOptions", context);
  for (const name of ["channelOptionsFor", "crossProviderModelOptions", "modelOptionsFor"]) {
    const options = loadFunction(name, context)("vision", {});
    assert.equal(options.some(option => option.value === ""), false);
  }
});
