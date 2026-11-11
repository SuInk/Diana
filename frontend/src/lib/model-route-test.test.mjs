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
    modelRouteTestTarget: { value: null }, modelRouteTests: { value: {} }, modelRouteTestMessage: "default message", visionTestMessage: "vision message",
    selectedRoleProfiles: () => profiles,
    isMediaRole: role => ["tts", "stt", "video"].includes(role),
    llmProviderLabel: value => value, describeLLMTestError: error => error.message,
    performance: { now: () => 1000 }, toastSuccess: () => {}, toastError: () => {},
    testLLM: async (message, config, mode = "text") => { calls.push({ mode, message, config }); return { text: "custom reply", usage: { input_tokens: 12, output_tokens: 34 } }; },
    testLLMImage: async (message, config) => { calls.push({ mode: "image", message, config }); return { images: ["https://example.com/result.png"], model: config.model }; }
  });
  for (const name of ["modelRouteTestKey", "modelRouteTestable", "modelRouteTestDefaultMessage", "setModelRouteTestKind", "testModelRoute", "runModelRouteTest"]) {
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

test("vision routes send the probe image request instead of plain text", async () => {
  const { context, calls } = setup();
  context.testModelRoute("vision", { profile_id: "p1", model: "eyes" });
  assert.equal(context.modelRouteTestTarget.value.message, "vision message");
  await context.runModelRouteTest();
  assert.equal(calls[0].mode, "vision");
  context.testModelRoute("chat", { profile_id: "p1", model: "chat" });
  await context.runModelRouteTest();
  assert.equal(calls[1].mode, "text");
});

test("switching test kind changes request mode and swaps untouched default messages", async () => {
  const { context, calls } = setup();
  context.testModelRoute("chat", { profile_id: "p1", model: "eyes" });
  const target = context.modelRouteTestTarget.value;
  assert.equal(target.kind, "text");
  context.setModelRouteTestKind("vision");
  assert.equal(target.message, "vision message");
  await context.runModelRouteTest();
  assert.equal(calls[0].mode, "vision");
  target.message = "我自己写的";
  context.setModelRouteTestKind("image");
  assert.equal(target.message, "我自己写的");
  await context.runModelRouteTest();
  assert.equal(calls[1].mode, "image");
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

test("group routes only call the first provider", async () => {
  const { context, calls } = setup([{ id: "p1" }, { id: "p2" }]);
  context.testModelRoute("chat", { group: "group", model: "model" });
  await context.runModelRouteTest();
  const target = context.modelRouteTestTarget.value;
  assert.equal(calls.length, 1);
  assert.equal(calls[0].config.id, "p1");
  assert.equal(target.results.length, 1);
  assert.match(context.modelRouteTests.value[target.key].label, /测试通过/);
});

test("empty image response is a failure, not a successful generation", async () => {
  const { context } = setup();
  context.testLLMImage = async () => ({ images: [] });
  context.testModelRoute("image", { model: "image" });
  await context.runModelRouteTest();
  assert.match(context.modelRouteTestTarget.value.results[0].error, /未返回图片/);
});

test("closing a running test cannot overwrite a new dialog", async () => {
  const { context } = setup();
  let finish;
  context.testLLM = () => new Promise(resolve => { finish = resolve; });
  context.testModelRoute("chat", { model: "old" });
  const old = context.modelRouteTestTarget.value;
  const running = context.runModelRouteTest();
  context.modelRouteTestTarget.value = null;
  context.testModelRoute("chat", { model: "new" });
  finish({ text: "old result" });
  await running;
  assert.equal(context.modelRouteTestTarget.value.model, "new");
  assert.equal(context.modelRouteTestTarget.value.results.length, 0);
  assert.equal(old.running, false);
});
