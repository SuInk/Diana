import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const script = parse(readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8")).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
function load(name, context) {
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return context[name];
}

test("new bot loads independent defaults and enables both switches", async () => {
  let form;
  const defaults = { enabled: true, owner_login_enabled: true, system_prompt: "default persona", onebot_access_token_configured: false };
  const context = vm.createContext({
    copiedFrom: { value: null }, busy: { value: false }, creating: { value: false }, platformPickerOpen: { value: true }, editorTab: { value: "model" }, page: { value: "list" },
    profiles: { value: [{ id: "old", system_prompt: "old persona", owner_id: "123", onebot_access_token_configured: true }] }, activeProfileID: { value: "old" },
    getNewBotProfileDefaults: async platform => { assert.equal(platform, "telegram"); return defaults; },
    setForm: value => { form = value; }, toastError: message => assert.fail(message)
  });
  await load("beginCreate", context)({ id: "telegram", name: "Telegram" });
  assert.equal(form.enabled, true);
  assert.equal(form.owner_login_enabled, true);
  assert.equal(form.owner_id, undefined);
  assert.equal(form.system_prompt, "default persona");
  assert.equal(form.onebot_access_token_configured, false);
  assert.equal(context.creating.value, true);
  assert.equal(context.page.value, "edit");
  assert.equal(context.busy.value, false);
});

test("failure to load defaults keeps the existing form untouched", async () => {
  const errors = [];
  const context = vm.createContext({ busy: { value: false }, getNewBotProfileDefaults: async () => { throw new Error("offline"); }, setForm: () => assert.fail("must not replace form"), toastError: message => errors.push(message) });
  await load("beginCreate", context)({ id: "onebot-v11" });
  assert.equal(errors.length, 1);
  assert.equal(context.busy.value, false);
});

test("duplicate address can become explicit reuse on an unsaved draft without losing behavior", () => {
  const source = { id: "source", name: "已有机器人" };
  const form = { value: { name: "新机器人", system_prompt: "my persona", owner_id: "42" } };
  const context = vm.createContext({ form, connectionConflict: { value: source }, profiles: { value: [source] } });
  const users = load("connectionUsers", context);
  assert.equal(users(form.value).length, 0);
  load("reuseConflictingConnection", context)();
  assert.equal(form.value.connection_profile_id, "source");
  assert.equal(form.value.name, "新机器人");
  assert.equal(form.value.system_prompt, "my persona");
  assert.equal(form.value.owner_id, "42");
  assert.equal(source.connection_profile_id, undefined);
});

test("a referenced source cannot be converted into a chain of shared connections", () => {
  const form = { value: { id: "source", name: "现有来源" } };
  const context = vm.createContext({ form, connectionConflict: { value: { id: "other" } }, profiles: { value: [{ id: "child", connection_profile_id: "source" }] } });
  load("connectionUsers", context);
  load("reuseConflictingConnection", context)();
  assert.equal(form.value.connection_profile_id, undefined);
});

test("copying opens a reviewable new draft without creating or activating a profile", async () => {
  const source = { id: "source", name: "来源", platform: "onebot-v11", connection_profile_id: "root", system_prompt: "persona" };
  let draft;
  const context = vm.createContext({ busy: { value: false }, copiedFrom: { value: null }, creating: { value: false }, platformPickerOpen: { value: true }, editorTab: { value: "model" }, page: { value: "list" }, getNewBotProfileDefaults: async () => ({ enabled: true }), copyBotConfiguration: (value) => ({ name: `${value.name} 副本`, system_prompt: value.system_prompt }), setForm: value => { draft = value; }, toastError: message => assert.fail(message) });
  await load("beginCopyProfile", context)(source);
  assert.equal(draft.name, "来源 副本");
  assert.equal(draft.id, undefined);
  assert.equal(draft.system_prompt, "persona");
  assert.equal(context.copiedFrom.value.connection_profile_id, "root");
  assert.equal(context.creating.value, true);
  assert.equal(context.page.value, "edit");
  assert.equal(context.editorTab.value, "access");
  assert.equal(context.platformPickerOpen.value, false);
  assert.equal(context.busy.value, false);
});
