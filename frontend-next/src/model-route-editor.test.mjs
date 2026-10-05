import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
const ast = ts.createSourceFile("view.ts", parse(source).descriptor.scriptSetup.content, ts.ScriptTarget.Latest, true);
function editorContext(roles) {
  const context = vm.createContext({
    defaultFollowChatRoles: ["vision", "intent", "image"],
    roleForm: { value: roles }, modelRouteEditor: { value: null }, modelRouteEditorError: { value: "" },
    modelRouteSearch: { value: "" }, modelRouteStatusFilter: { value: "all" },
    llmChannels: { value: [{ id: "p1", name: "主提供商" }, { id: "p2", name: "后备提供商" }] },
    FOLLOW_CHAT: "__follow_chat__", FOLLOW_VISION: "__follow_vision__", GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::",
    selectedRoleProfiles: (_, route) => ["p1", "p2"].includes(route.profile_id) || route.group === "shared" ? [{}] : [],
    profileCanRouteRoleModel: (_, __, model) => model === "m1" || model === "m2",
    modelOptionsFor: (_, route) => [{ value: route.profile_id === "p2" ? "m2" : "m1" }]
  });
  for (const name of ["roleSnapshot", "modelRoleRoutes", "modelRouteIsStandby", "modelRouteProviderLabel", "filteredModelRoutes", "clearModelRouteFilters", "openModelRouteEditor", "setModelRouteEditorChannel", "setModelRouteEditorModel", "applyModelRouteEditor"]) {
    const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
    assert.ok(fn, name);
    vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  }
  return context;
}

test("route editor cancellation keeps primary, fallbacks and follow mode unchanged", () => {
  const roles = { chat: { profile_id: "p1", model: "m1", routing_strategy: "weighted", fallbacks: [{ profile_id: "p2", model: "m2", standby: true }] }, vision: { model: "", follow_chat: true } };
  const context = editorContext(roles);
  const before = JSON.stringify(roles);
  context.openModelRouteEditor("chat", 0);
  assert.equal(context.modelRouteEditor.value.draft.fallbacks, undefined);
  context.setModelRouteEditorChannel("p2");
  context.modelRouteEditor.value.draft.disabled = true;
  context.modelRouteEditor.value = null;
  context.openModelRouteEditor("vision", 0);
  context.setModelRouteEditorModel("p2::m2");
  context.modelRouteEditor.value = null;
  assert.equal(JSON.stringify(roles), before);
});

test("editing a primary preserves its scheduling strategy and fallback metadata", () => {
  const roles = { chat: { profile_id: "p1", model: "m1", weight: 3, routing_strategy: "weighted", fallbacks: [{ profile_id: "p2", model: "m2", weight: 2, disabled: true, standby: true }] } };
  const context = editorContext(roles);
  context.openModelRouteEditor("chat", 0);
  context.setModelRouteEditorChannel("p2");
  context.modelRouteEditor.value.draft.weight = 2000;
  context.applyModelRouteEditor();
  assert.equal(roles.chat.model, "m2");
  assert.equal(roles.chat.weight, 1000);
  assert.equal(roles.chat.routing_strategy, "weighted");
  assert.equal(roles.chat.fallbacks[0].standby, true);
  assert.equal(roles.chat.fallbacks[0].disabled, true);
  assert.equal(context.modelRouteEditor.value, null);
});

test("adding and editing a fallback keeps its index and scheduling metadata", () => {
  const roles = { chat: { profile_id: "p1", model: "m1", routing_strategy: "weighted" } };
  const context = editorContext(roles);
  context.openModelRouteEditor("chat");
  context.setModelRouteEditorModel("p2::m2");
  context.modelRouteEditor.value.draft.standby = true;
  context.applyModelRouteEditor();
  assert.equal(roles.chat.fallbacks.length, 1);
  context.openModelRouteEditor("chat", 1);
  context.modelRouteEditor.value.draft.disabled = true;
  context.applyModelRouteEditor();
  assert.equal(roles.chat.fallbacks.length, 1);
  assert.equal(roles.chat.fallbacks[0].standby, true);
  assert.equal(roles.chat.fallbacks[0].disabled, true);
  assert.equal(roles.chat.profile_id, "p1");
});

test("inherited models become independent only after a valid route is applied", () => {
  const roles = { vision: { model: "", follow_chat: true } };
  const context = editorContext(roles);
  context.openModelRouteEditor("vision");
  context.applyModelRouteEditor();
  assert.equal(roles.vision.follow_chat, true);
  assert.ok(context.modelRouteEditorError.value);
  context.setModelRouteEditorModel("p1::m1");
  context.applyModelRouteEditor();
  assert.equal(roles.vision.follow_chat, undefined);
  assert.equal(roles.vision.model, "m1");
});

test("an incoming configuration update prevents a stale modal from overwriting it", () => {
  const roles = { chat: { profile_id: "p1", model: "m1" } };
  const context = editorContext(roles);
  context.openModelRouteEditor("chat", 0);
  roles.chat.model = "m2";
  context.applyModelRouteEditor();
  assert.equal(roles.chat.model, "m2");
  assert.match(context.modelRouteEditorError.value, /别处更新/);
  assert.ok(context.modelRouteEditor.value);
});

test("model filters retain original indices and distinguish disabled and standby routes", () => {
  const roles = { chat: { profile_id: "p1", model: "m1", routing_strategy: "weighted", fallbacks: [{ profile_id: "p2", model: "m2", disabled: true }, { group: "shared", model: "m1", standby: true }] } };
  const context = editorContext(roles);
  context.modelRouteSearch.value = "后备提供商";
  assert.equal(context.filteredModelRoutes("chat")[0].index, 1);
  context.modelRouteSearch.value = "";
  context.modelRouteStatusFilter.value = "standby";
  assert.equal(context.filteredModelRoutes("chat")[0].index, 2);
  context.modelRouteStatusFilter.value = "disabled";
  assert.equal(context.filteredModelRoutes("chat")[0].index, 1);
  roles.chat.routing_strategy = undefined;
  context.modelRouteStatusFilter.value = "standby";
  assert.equal(context.filteredModelRoutes("chat").length, 2);
});
