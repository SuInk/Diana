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

const isMediaRole = role => ["tts", "stt", "video"].includes(role);

test("saving requires an explicit provider and model for each role", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  for (const key of keys) {
    for (const invalid of [undefined, { model: "m" }, { profile_id: "p", model: "" }]) {
      const errors = [];
      const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
      roles[key] = invalid;
      const busy = { value: false };
      const context = vm.createContext({ connectionConflict: { value: undefined }, form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } }, roleForm: { value: roles }, modelRoleRows: keys.map(key => ({ key, label: key })), purposeRoleRows: [], purposeRoleKeys: [], visibleModelRoleRows: keys.map(key => ({ key, label: key })), editorTab: { value: "access" }, validWebSocketURL: () => true, roleModelIsSelectable: () => true, sendRetryValidationError: () => "", toastError: message => errors.push(message), busy, isMediaRole });
      await loadFunction("save", context)();
      assert.equal(busy.value, false);
      assert.equal(errors.length, 1);
      assert.ok(errors[0].startsWith(key));
    }
  }
});

test("provider and model menus do not offer an empty assignment", () => {
  const context = vm.createContext({ roleForm: {value:{}}, llmChannels: { value: [] }, channelGroups: () => [], selectedRoleProfiles: () => [], modelsForRole: () => [], modelRoleRows: [], GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::", FOLLOW_CHAT: "__follow_chat__", isMediaRole });
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
    isMediaRole,
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
  // 音视频插槽没有可跟随的对话模型：对话模型接不了 /audio/speech 这些接口。
  for (const role of ["tts", "stt", "video"]) {
    assert.equal(channelOptionsFor(role).some(option => option.value === "__follow_chat__"), false);
  }

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

// 细分用途留空表示「跟随意图识别」，不能被当成漏配拦下保存。
test("purpose-level roles may be left unset", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  const errors = [];
  const busy = { value: false };
  const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
  const context = vm.createContext({
    connectionConflict: { value: undefined },
    form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } },
    roleForm: { value: roles },
    modelRoleRows: keys.map(key => ({ key, label: key })),
    purposeRoleRows: [{ key: "reply_account_safety", label: "发送前审核" }, { key: "memory_extract", label: "记忆抽取" }],
    purposeRoleKeys: ["reply_account_safety", "memory_extract"],
    visibleModelRoleRows: [...keys.map(key => ({ key, label: key })), { key: "reply_account_safety", label: "发送前审核" }, { key: "memory_extract", label: "记忆抽取" }],
    editorTab: { value: "access" },
    validWebSocketURL: () => true,
    roleModelIsSelectable: () => true,
    sendRetryValidationError: () => "",
    toastError: message => errors.push(message),
    busy
  });
  // save 走过校验之后还会碰上沙箱里没有的依赖，这里只关心校验这一段：
  // 留空的细分用途不该产生任何针对它的报错。
  await loadFunction("save", context)().catch(() => {});
  const complaints = errors.filter((message) => message.startsWith("发送前审核") || message.startsWith("记忆抽取"));
  assert.deepEqual(complaints, [], `细分用途留空不该报错：${errors.join(" | ")}`);
});

// 配了的细分用途照常校验：指了提供商却没选模型要拦下来。
test("a configured purpose role still needs a model", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  const errors = [];
  const busy = { value: false };
  const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
  roles.reply_account_safety = { profile_id: "p", model: "" };
  const context = vm.createContext({
    connectionConflict: { value: undefined },
    form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } },
    roleForm: { value: roles },
    modelRoleRows: keys.map(key => ({ key, label: key })),
    purposeRoleRows: [{ key: "reply_account_safety", label: "发送前审核" }],
    purposeRoleKeys: ["reply_account_safety"],
    visibleModelRoleRows: [...keys.map(key => ({ key, label: key })), { key: "reply_account_safety", label: "发送前审核" }],
    editorTab: { value: "access" },
    validWebSocketURL: () => true,
    roleModelIsSelectable: () => true,
    sendRetryValidationError: () => "",
    toastError: message => errors.push(message),
    busy
  });
  await loadFunction("save", context)();
  assert.equal(errors.length, 1);
  assert.ok(errors[0].startsWith("发送前审核"), errors[0]);
});

// 音视频插槽不配就是不启用，不能被当成漏配拦下保存；配了没选模型照常拦。
test("media slots may be left unset but a configured slot needs a model", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  const media = [{ key: "tts", label: "语音合成" }, { key: "stt", label: "语音识别" }, { key: "video", label: "视频生成" }];
  for (const [ttsRole, expected] of [[undefined, 0], [{ profile_id: "p", model: "" }, 1]]) {
    const errors = [];
    const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
    if (ttsRole) roles.tts = ttsRole;
    const context = vm.createContext({
      connectionConflict: { value: undefined },
      form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } },
      roleForm: { value: roles },
      modelRoleRows: keys.map(key => ({ key, label: key })),
      purposeRoleRows: [],
      purposeRoleKeys: [],
      visibleModelRoleRows: [...keys.map(key => ({ key, label: key })), ...media],
      isMediaRole,
      editorTab: { value: "access" },
      validWebSocketURL: () => true,
      roleModelIsSelectable: () => true,
      sendRetryValidationError: () => "",
      toastError: message => errors.push(message),
      busy: { value: false }
    });
    await loadFunction("save", context)().catch(() => {});
    const complaints = errors.filter(message => media.some(row => message.startsWith(row.label)));
    assert.equal(complaints.length, expected, errors.join(" | "));
  }
});
