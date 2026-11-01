import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const fn = ast.statements.find(n => ts.isFunctionDeclaration(n) && n.name?.text === "resetReplyFilterRules");
assert.ok(fn);
function setup(overrides = {}) {
  const context = vm.createContext({
    form: { value: { platform: "telegram", name: "unchanged" } },
    replyFilterRulesDraft: { value: ["custom"] },
    replyFilterResetBusy: { value: false },
    replyFilterTestResult: { value: { result: "old preview" } },
    askConfirm: async () => true,
    getNewBotProfileDefaults: async platform => {
      assert.equal(platform, "telegram");
      return { reply_filter_rules: ["server-default"] };
    },
    toastSuccess() {}, toastError() {}, ...overrides
  });
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return context;
}
test("restore uses current server defaults, only changes draft and clears preview", async () => {
  const c = setup();
  await c.resetReplyFilterRules();
  assert.equal(JSON.stringify(c.replyFilterRulesDraft.value), '["server-default"]');
  assert.equal(c.form.value.name, "unchanged");
  assert.equal(c.replyFilterTestResult.value, null);
  assert.equal(c.replyFilterResetBusy.value, false);
});
test("cancel, missing defaults and failed request preserve custom rules", async () => {
  for (const overrides of [
    { askConfirm: async () => false },
    { getNewBotProfileDefaults: async () => ({}) },
    { getNewBotProfileDefaults: async () => { throw Error("offline"); } }
  ]) {
    const c = setup(overrides);
    await c.resetReplyFilterRules();
    assert.equal(JSON.stringify(c.replyFilterRulesDraft.value), '["custom"]');
    assert.equal(c.replyFilterResetBusy.value, false);
  }
});
test("late response cannot overwrite another profile or newer edits", async () => {
  for (const change of [c => { c.form.value = { platform: "telegram" }; }, c => { c.replyFilterRulesDraft.value = ["new edit"]; }]) {
    const c = setup();
    c.getNewBotProfileDefaults = async () => { change(c); return { reply_filter_rules: ["server-default"] }; };
    await c.resetReplyFilterRules();
    assert.notEqual(JSON.stringify(c.replyFilterRulesDraft.value), '["server-default"]');
  }
});
test("demo defaults match Go defaults and UI documents current markup", () => {
  const go = readFileSync(new URL("../../../model/assistant/reply_filter.go", import.meta.url), "utf8");
  const demo = readFileSync(new URL("../demo/demo.ts", import.meta.url), "utf8");
  const rule = go.match(/return \[\]string\{`([^`]+)`\}/)[1];
  assert.ok(demo.includes('reply_filter_rules: [String.raw`' + rule + '`]'));
  assert.ok(source.includes("&lt;diana-single/&gt;"));
  assert.ok(!source.includes("[DIANA_REPLY_SINGLE]"));
});
