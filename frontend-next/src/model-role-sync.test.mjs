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

const ROLE_ROWS = ["chat", "vision", "media_parse", "intent", "image"];

function editorContext() {
  const context = vm.createContext({
    form: { value: { id: "bot" } },
    roleForm: { value: {} },
    savedRoleSnapshot: { value: "" },
    modelRolesChangedElsewhere: { value: false },
    incomingModelRoles: { value: undefined },
    modelRoleRows: ROLE_ROWS.map(key => ({ key }))
  });
  for (const name of ["orderedRoleKeys", "roleSnapshot", "setRoleForm", "syncModelRolesWhileEditing", "adoptIncomingModelRoles"]) {
    loadFunction(name, context);
  }
  return context;
}

test("an untouched model assignment follows a change made elsewhere", () => {
  const context = editorContext();
  context.setRoleForm({ chat: { profile_id: "p", model: "old" } });
  // 主人在聊天里换了模型：草稿里这一档没动过，直接跟上，页面不用手动刷新。
  context.syncModelRolesWhileEditing({ id: "bot", model_roles: { chat: { profile_id: "p", model: "new" } } });
  assert.equal(context.roleForm.value.chat.model, "new");
  assert.equal(context.modelRolesChangedElsewhere.value, false);
  // 编辑的不一定是当前激活的那台机器人，配置列表里也要能找到。
  context.syncModelRolesWhileEditing({ id: "other", profiles: [{ id: "bot", model_roles: { chat: { profile_id: "p", model: "newest" } } }] });
  assert.equal(context.roleForm.value.chat.model, "newest");
});

test("a touched model assignment is kept and the conflict is surfaced instead", () => {
  const context = editorContext();
  context.setRoleForm({ chat: { profile_id: "p", model: "old" } });
  context.roleForm.value.chat.model = "draft";
  context.syncModelRolesWhileEditing({ id: "bot", model_roles: { chat: { profile_id: "p", model: "new" } } });
  assert.equal(context.roleForm.value.chat.model, "draft");
  assert.equal(context.modelRolesChangedElsewhere.value, true);
  // 提示里那颗按钮：主人自己决定放弃草稿，换成服务端最新的。
  context.adoptIncomingModelRoles();
  assert.equal(context.roleForm.value.chat.model, "new");
  assert.equal(context.modelRolesChangedElsewhere.value, false);
});

test("a refresh that carries no actual change leaves the editor alone", () => {
  const context = editorContext();
  context.setRoleForm({ chat: { profile_id: "p", model: "old" } });
  context.roleForm.value.chat.model = "draft";
  context.syncModelRolesWhileEditing({ id: "bot", model_roles: { chat: { profile_id: "p", model: "old" } } });
  assert.equal(context.roleForm.value.chat.model, "draft");
  assert.equal(context.modelRolesChangedElsewhere.value, false);
});

test("model assignments keep the page's own order whatever order they arrive in", () => {
  const context = editorContext();
  // 服务端那份是个 map，序列化出来按字母排；页面不跟着它排。
  context.setRoleForm({ intent: { profile_id: "p", model: "i" }, chat: { profile_id: "p", model: "c" }, vision: { profile_id: "p", model: "v" } });
  assert.deepEqual(Object.keys(context.roleForm.value), ["chat", "vision", "intent"]);
  // 编辑途中被别处的改动整份换掉，键序同样不变。
  context.syncModelRolesWhileEditing({ id: "bot", model_roles: { vision: { profile_id: "p", model: "v2" }, chat: { profile_id: "p", model: "c" }, intent: { profile_id: "p", model: "i" } } });
  assert.deepEqual(Object.keys(context.roleForm.value), ["chat", "vision", "intent"]);
  // 认不出的用途排在后面，不会被丢掉。
  context.setRoleForm({ future: { profile_id: "p", model: "f" }, chat: { profile_id: "p", model: "c" } });
  assert.deepEqual(Object.keys(context.roleForm.value), ["chat", "future"]);
});
