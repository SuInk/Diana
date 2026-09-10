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
  const context = vm.createContext({ roleForm: {value:{}}, llmChannels: { value: [] }, channelGroups: () => [], selectedRoleProfiles: () => [], modelsForRole: () => [], modelRoleRows: [], GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::", FOLLOW_CHAT: "__follow_chat__" });
  loadFunction("crossProviderModelOptions", context);
  for (const name of ["channelOptionsFor", "crossProviderModelOptions", "modelOptionsFor"]) {
    const options = loadFunction(name, context)("vision", {});
    assert.equal(options.some(option => option.value === ""), false);
  }
});

test("provider dropdown offers follow chat for every role except chat", () => {
  const roleForm = { value: { vision: { profile_id: "old", model: "old", fallbacks: [{ profile_id: "backup", model: "old-backup" }] } } };
  const context = vm.createContext({
    roleForm,
    GROUP_PREFIX: "group:",
    MODEL_PAIR_SEP: "::",
    FOLLOW_CHAT: "__follow_chat__",
    llmChannels: { value: [] },
    channelGroups: () => [],
    modelsForRole: () => [],
    selectedRoleProfiles: () => [],
    crossProviderModelOptions: () => [{ value: "p::real", label: "Real" }],
    roleModelIsSelectable: () => true
  });
  const channelOptionsFor = loadFunction("channelOptionsFor", context);
  // 「跟随对话」搬到了提供商一栏，而且不再是视觉理解的特权。
  for (const role of ["vision", "intent", "image"]) {
    assert.equal(channelOptionsFor(role)[0].value, "__follow_chat__");
    assert.equal(channelOptionsFor(role)[0].label, "跟随对话");
  }
  assert.equal(channelOptionsFor("chat").some(option => option.value === "__follow_chat__"), false);

  loadFunction("modelOptionsFor", context);
  loadFunction("setRoleChannel", context)("vision", "__follow_chat__");
  // 跟随之后不留自己的提供商、模型和后备：这三样都从对话那一档现取。
  assert.equal(roleForm.value.vision.follow_chat, true);
  assert.equal(roleForm.value.vision.profile_id, undefined);
  assert.equal(roleForm.value.vision.fallbacks, undefined);
  assert.equal(loadFunction("routeSelectionValue", context)(roleForm.value.vision), "__follow_chat__");
  // 模型一栏锁定：没有可选项，值留空让 placeholder 说明它跟着谁。
  assert.equal(context.modelOptionsFor("vision").length, 0);
  assert.equal(loadFunction("roleModelValue", context)("vision"), "");

  // 切回具体提供商，跟随解除，模型重新可选。
  context.setRoleChannel("vision", "p1");
  assert.equal(roleForm.value.vision.follow_chat, undefined);
  assert.equal(roleForm.value.vision.profile_id, "p1");
});

test("persona generator picks its own provider and model, defaulting to follow chat", () => {
  const personaRoute = { value: undefined };
  const context = vm.createContext({
    personaRoute,
    GROUP_PREFIX: "group:",
    MODEL_PAIR_SEP: "::",
    FOLLOW_CHAT: "__follow_chat__",
    personaModelOptions: { value: [{ value: "m1" }, { value: "m2" }] }
  });
  const setPersonaChannel = loadFunction("setPersonaChannel", context);
  loadFunction("setPersonaModel", context);

  setPersonaChannel("p1");
  assert.equal(personaRoute.value.profile_id, "p1");
  // 原来没有模型，换提供商后就近挑一个能用的，不留空组合。
  assert.equal(personaRoute.value.model, "m1");

  context.setPersonaModel("m2");
  assert.equal(personaRoute.value.model, "m2");

  setPersonaChannel("group:default");
  assert.equal(personaRoute.value.group, "default");
  assert.equal(personaRoute.value.profile_id, undefined);

  // 跟随对话用 undefined 表示，调用点据此回落到对话那一档。
  setPersonaChannel("__follow_chat__");
  assert.equal(personaRoute.value, undefined);
  // 跟随时模型不可改。
  context.setPersonaModel("m1");
  assert.equal(personaRoute.value, undefined);
});
