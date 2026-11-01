import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
function setup(profiles = [{ id: "p1", name: "主提供商", provider: "openai_compatible" }]) {
  const calls = [];
  const context = vm.createContext({
    modelRouteTestTarget: { value: null }, modelRouteTests: { value: {} }, modelRouteTestMessage: "default message",
    selectedRoleProfiles: () => profiles,
    isMediaRole: role => ["tts", "stt", "video"].includes(role),
    llmProviderLabel: value => value, describeLLMTestError: error => error.message,
    performance: { now: () => 1000 }, toastSuccess: () => {}, toastError: () => {},
    testLLM: async (message, config) => { calls.push({ mode: "text", message, config }); return { text: "custom reply", usage: { input_tokens: 12, output_tokens: 34 } }; },
    testLLMImage: async (message, config) => { calls.push({ mode: "image", message, config }); return { images: ["https://example.com/result.png"], model: config.model }; }
  });
  for (const name of ["modelRouteTestKey", "modelRouteTestable", "testModelRoute", "runModelRouteTest"]) {
    const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
    vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  }
  return { context, calls };
}

test("opening a route test never sends a request; custom message and selected model are used", async () => {
  const { context, calls } = setup();
  context.testModelRoute("chat", { profile_id: "p1", model: " selected-model " });
  assert.equal(calls.length, 0);
  context.modelRouteTestTarget.value.message = "  解释一下这段代码  ";
  await context.runModelRouteTest();
  assert.equal(calls[0].message, "解释一下这段代码");
  assert.equal(calls[0].config.model, "selected-model");
  assert.equal(calls[0].config.id, "p1");
  const result = context.modelRouteTestTarget.value.results[0];
  assert.equal(result.text, "custom reply");
  assert.match(result.usage, /12.*34/);
});

test("image routes expose testing and show images from the image API", async () => {
  const { context, calls } = setup();
  assert.equal(context.modelRouteTestable("image", { model: "image-model" }), true);
  assert.equal(context.modelRouteTestable("tts", { model: "speech" }), false);
  context.testModelRoute("image", { profile_id: "p1", model: "image-model" });
  context.modelRouteTestTarget.value.message = "雨夜中的猫";
  await context.runModelRouteTest();
  assert.equal(calls[0].mode, "image");
  assert.equal(calls[0].message, "雨夜中的猫");
  assert.equal(calls[0].config.model, "image-model");
  assert.equal(context.modelRouteTestTarget.value.results[0].images[0], "https://example.com/result.png");
});

test("empty prompts and duplicate clicks do not trigger requests", async () => {
  const { context, calls } = setup();
  context.testModelRoute("chat", { model: "model" });
  context.modelRouteTestTarget.value.message = " \n ";
  await context.runModelRouteTest();
  context.modelRouteTestTarget.value.message = "hello";
  context.modelRouteTestTarget.value.running = true;
  await context.runModelRouteTest();
  assert.equal(calls.length, 0);
});

test("group tests keep each provider result even if one fails", async () => {
  const { context } = setup([{ id: "p1" }, { id: "p2" }]);
  context.testLLM = async (_, config) => {
    if (config.id === "p1") throw new Error("upstream failed");
    return { text: "second reply" };
  };
  context.testModelRoute("chat", { group: "group", model: "model" });
  await context.runModelRouteTest();
  const target = context.modelRouteTestTarget.value;
  assert.equal(target.results[0].error, "upstream failed");
  assert.equal(target.results[1].text, "second reply");
  assert.equal(context.modelRouteTests.value[target.key].label, "1/2 失败");
});

test("empty image response is a failure, not a successful generation", async () => {
  const { context } = setup();
  context.testLLMImage = async () => ({ images: [] });
  context.testModelRoute("image", { model: "image" });
  await context.runModelRouteTest();
  assert.match(context.modelRouteTestTarget.value.results[0].error, /未返回图片/);
});

test("closing a running test stops remaining providers and cannot overwrite a new dialog", async () => {
  const { context } = setup([{ id: "p1" }, { id: "p2" }]);
  let finish;
  let requests = 0;
  context.testLLM = () => { requests++; return new Promise(resolve => { finish = resolve; }); };
  context.testModelRoute("chat", { model: "old" });
  const old = context.modelRouteTestTarget.value;
  const running = context.runModelRouteTest();
  context.modelRouteTestTarget.value = null;
  context.testModelRoute("chat", { model: "new" });
  finish({ text: "old result" });
  await running;
  assert.equal(requests, 1);
  assert.equal(context.modelRouteTestTarget.value.model, "new");
  assert.equal(context.modelRouteTestTarget.value.results.length, 0);
  assert.equal(context.modelRouteTests.value[old.key].label, "测试已停止");
  assert.equal(old.running, false);
});
