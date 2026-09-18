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
    busy: { value: false }, creating: { value: false }, platformPickerOpen: { value: true }, editorTab: { value: "model" }, page: { value: "list" },
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
