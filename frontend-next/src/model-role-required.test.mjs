import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";
import { effectiveModelInfo, mergeModelMetadata } from "./model-metadata.ts";

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
const isPurposeRole = role => ["reply_assist", "background"].includes(role);
const purposeRoleFallbackLabel = role => (role === "reply_assist" ? "后台生成" : "对话");
const purposeHelpers = { isPurposeRole, purposeRoleFallbackLabel, UNSPECIFIED: "__unspecified__" };
test("every role uses the edited capabilities, including text-only background models", () => {
  const context = vm.createContext({ ...purposeHelpers, isMediaRole, effectiveModelInfo, mergeModelMetadata });
  loadFunction("normalizedModalities", context);
  loadFunction("mergeModelInfo", context);
  const compatibility = loadFunction("modelCompatibility", context);
  const text = { id: "m", input_modalities: ["text", "image"], output_modalities: ["text"], capabilities_override: { input_modalities: ["text"], output_modalities: ["text"] } };
  for (const role of ["chat", "intent", "background"]) assert.equal(compatibility(text, role), "compatible");
  for (const role of ["vision", "media_parse", "image"]) assert.equal(compatibility(text, role), "incompatible");
  const image = { ...text, capabilities_override: { input_modalities: ["text", "image"], output_modalities: ["image"] } };
  assert.equal(compatibility(image, "image"), "compatible");
  assert.equal(compatibility(image, "chat"), "incompatible");
  const vision = { ...text, capabilities_override: { input_modalities: ["text", "image"], output_modalities: ["text"] } };
  for (const role of ["chat", "vision", "media_parse"]) assert.equal(compatibility(vision, role), "compatible");
  for (const role of ["chat", "vision", "image", "background"]) assert.equal(compatibility({ id: "unknown" }, role), "unknown");
  const refreshed = context.mergeModelInfo({ id: "m", input_modalities: ["text", "image"], output_modalities: ["text"] }, text);
  assert.equal(compatibility(refreshed, "vision"), "incompatible");
});

test("mismatched capabilities remain selectable in every provider, group and cross-provider menu", () => {
  const models = [
    { id: "text", input_modalities: ["text"], output_modalities: ["text"] },
    { id: "image", input_modalities: ["text"], output_modalities: ["image"] },
    { id: "unknown", custom: true }
  ];
  const profiles = [{ id: "p", name: "P", group: "pool", provider: "openai_compatible", model: "text", models }, { id: "backup", name: "Backup", group: "pool", provider: "openai_compatible", model: "text", models }];
  const context = vm.createContext({ ...purposeHelpers, isMediaRole, effectiveModelInfo, mergeModelMetadata, llmChannels: { value: profiles }, roleForm: { value: {} }, MODEL_PAIR_SEP: "::", GROUP_PREFIX: "group:", FOLLOW_CHAT: "__follow_chat__", FOLLOW_VISION: "__follow_vision__" });
  for (const name of ["normalizedModalities", "mergeModelInfo", "profileModels", "modelCompatibility", "compatibilityRank", "modelCapabilityLabel", "modelHint", "modelsForRole", "llmProviderLabel", "channelGroups", "channelOptionsFor", "selectedRoleProfiles", "profileCanRouteRoleModel", "roleModelIsSelectable", "crossProviderModelOptions", "modelOptionsFor"]) loadFunction(name, context);
  for (const role of ["chat", "vision", "intent", "image", "media_parse", "background"]) {
    const options = context.modelOptionsFor(role, { profile_id: "p" });
    assert.equal(options.length, 3, role);
    assert.equal(options.some(option => option.disabled), false);
    const groupOptions = context.modelOptionsFor(role, { group: "pool" });
    assert.equal(groupOptions.length, 3, role);
    assert.equal(context.crossProviderModelOptions(role).length, 6, role);
    context.roleForm.value[role] = { profile_id: "p", model: "text" };
    for (const model of models) assert.equal(context.roleModelIsSelectable(role, model.id), true, `${role}/${model.id}`);
    assert.equal(context.profileCanRouteRoleModel(profiles[0], role, "not-configured"), false);
  }
  assert.match(context.modelOptionsFor("vision", { profile_id: "p" }).find(option => option.value === "text").hint, /仍可选择/);
});

test("saving requires an explicit provider and model for each role", async () => {
  const keys = ["chat", "vision", "intent", "image"];
  for (const key of keys) {
    for (const invalid of [undefined, { model: "m" }, { profile_id: "p", model: "" }]) {
      const errors = [];
      const roles = Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }]));
      roles[key] = invalid;
      const busy = { value: false };
      const context = vm.createContext({ connectionConflict: { value: undefined }, form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } }, roleForm: { value: roles }, modelRoleRows: keys.map(key => ({ key, label: key })), purposeRoleRows: [], purposeRoleKeys: [], selectedModelRole: { value: "chat" }, visibleModelRoleRows: keys.map(key => ({ key, label: key })), editorTab: { value: "access" }, validWebSocketURL: () => true, roleModelIsSelectable: () => true, sendRetryValidationError: () => "", toastError: message => errors.push(message), busy, isMediaRole });
      await loadFunction("save", context)();
      assert.equal(busy.value, false);
      assert.equal(errors.length, 1);
      assert.ok(errors[0].startsWith(key));
    }
  }
});

test("provider and model menus do not offer an empty assignment", () => {
  const context = vm.createContext({ roleForm: {value:{}}, llmChannels: { value: [] }, channelGroups: () => [], selectedRoleProfiles: () => [], modelsForRole: () => [], modelRoleRows: [], GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::", FOLLOW_CHAT: "__follow_chat__", DISABLED: "__disabled__", isMediaRole, ...purposeHelpers });
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
    DISABLED: "__disabled__",
    isMediaRole,
    ...purposeHelpers,
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
    selectedModelRole: { value: "chat" }, editorTab: { value: "access" },
    visibleModelRoleRows: [...keys.map(key => ({ key, label: key })), { key: "reply_account_safety", label: "发送前审核" }, { key: "memory_extract", label: "记忆抽取" }],
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
    selectedModelRole: { value: "chat" }, editorTab: { value: "access" },
    visibleModelRoleRows: [...keys.map(key => ({ key, label: key })), { key: "reply_account_safety", label: "发送前审核" }],
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
      selectedModelRole: { value: "chat" },
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

test("optional purpose roles can go back to unspecified", () => {
  const roleForm = { value: { reply_assist: { profile_id: "p", model: "m" } } };
  const context = vm.createContext({
    roleForm,
    GROUP_PREFIX: "group:",
    FOLLOW_CHAT: "__follow_chat__",
    DISABLED: "__disabled__",
    isMediaRole,
    ...purposeHelpers,
    llmChannels: { value: [] },
    channelGroups: () => []
  });
  const channelOptionsFor = loadFunction("channelOptionsFor", context);
  // 选过一次提供商之后也要能退回「不指定」，否则只能跟随对话，跟不回后台生成。
  const first = channelOptionsFor("reply_assist")[0];
  assert.equal(first.value, "__unspecified__");
  assert.equal(first.label, "不指定，跟随后台生成");
  assert.equal(channelOptionsFor("background")[0].label, "不指定，跟随对话");
  assert.equal(channelOptionsFor("vision").some(option => option.value === "__unspecified__"), false);
  loadFunction("routeSelectionValue", context);
  const roleSelectionValue = loadFunction("roleSelectionValue", context);
  assert.equal(roleSelectionValue("reply_assist"), "p");
  loadFunction("setRoleChannel", context)("reply_assist", "__unspecified__");
  assert.equal(roleForm.value.reply_assist, undefined);
  assert.equal(roleSelectionValue("reply_assist"), "__unspecified__");
});

test("changing provider or promoting a channel preserves balancing settings", () => {
  const roleForm = { value: { chat: { profile_id: "a", model: "m", routing_strategy: "weighted", weight: 3, disabled: true, fallbacks: [{ profile_id: "b", model: "m", weight: 2, standby: true }] } } };
  const context = vm.createContext({ ...purposeHelpers, isMediaRole, roleForm, GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::", FOLLOW_CHAT: "__follow_chat__", roleModelIsSelectable: () => true, modelOptionsFor: () => [{ value: "m" }] });
  loadFunction("setRoleChannel", context)("chat", "c");
  assert.equal(roleForm.value.chat.routing_strategy, "weighted");
  assert.equal(roleForm.value.chat.weight, 3);
  assert.equal(roleForm.value.chat.disabled, true);
  loadFunction("setRoleModel", context)("chat", "d::m2");
  assert.equal(roleForm.value.chat.routing_strategy, "weighted");
  assert.equal(roleForm.value.chat.weight, 3);
  loadFunction("moveRoleRoute", context)("chat", 1, 0);
  assert.equal(roleForm.value.chat.profile_id, "b");
  assert.equal(roleForm.value.chat.weight, 2);
  assert.equal(roleForm.value.chat.standby, undefined);
  assert.equal(roleForm.value.chat.routing_strategy, "weighted");
  assert.equal(roleForm.value.chat.fallbacks[0].disabled, true);
  assert.equal(roleForm.value.chat.fallbacks[0].routing_strategy, undefined);
});

test("all-disabled model routes are rejected and their purpose is opened", async () => {
  const errors = [];
  const keys = ["chat", "vision", "intent", "image"];
  const roleForm = { value: Object.fromEntries(keys.map(key => [key, { profile_id: "p", model: "m" }])) };
  roleForm.value.intent.disabled = true;
  roleForm.value.intent.fallbacks = [{ profile_id: "p2", model: "m", disabled: true }];
  const selectedModelRole = { value: "chat" };
  const context = vm.createContext({ connectionConflict: { value: undefined }, form: { value: { onebot_reverse_ws_endpoint: "ws://localhost" } }, roleForm, modelRoleRows: keys.map(key => ({ key, label: key })), purposeRoleRows: [], purposeRoleKeys: [], editorTab: { value: "access" }, selectedModelRole, visibleModelRoleRows: keys.map(key => ({ key, label: key })), isMediaRole, sendRetryValidationError: () => "", validWebSocketURL: () => true, roleModelIsSelectable: () => true, toastError: message => errors.push(message), busy: { value: false } });
  await loadFunction("save", context)();
  assert.equal(errors[0], "intent至少需要启用一个模型");
  assert.equal(selectedModelRole.value, "intent");
});
