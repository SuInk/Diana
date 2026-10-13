import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";
import { modelRequestTimeoutSeconds } from "./configuration-sync.ts";

const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
test("model scheduling explains health timing and boundaries without an extra switch", () => {
  const disclosure = source.match(/<AppDisclosure :id="`model-description-\$\{role.key\}`"[\s\S]*?<\/AppDisclosure>/)?.[0];
  assert.ok(disclosure);
  for (const text of ["700 毫秒重试 1 次", "首次冷却 10 秒", "首次冷却 1 分钟", "单次冷却最多 30 分钟", "有效 Retry-After 优先", "只提前探测最早恢复的一个", "只有一个候选时仍允许调用", "不读取或推算剩余额度", "已完成的工具操作不会因切换重复执行"]) {
    assert.ok(disclosure.includes(text), text);
  }
  assert.ok(disclosure.includes('v-if="!isMediaRole(role.key) && role.key !== \'image\'"'));
  assert.doesNotMatch(disclosure, /<AppSwitch/);
});

test("model route table exposes live cooling and degraded health states", () => {
  assert.match(source, /getModelHealth/);
  assert.match(source, /冷却中/);
  assert.match(source, /已降权/);
  assert.match(source, /10_000/);
  assert.match(source, /model-health-badge/);
});

const ast = ts.createSourceFile("view.ts", parse(source).descriptor.scriptSetup.content, ts.ScriptTarget.Latest, true);
function editorContext(roles) {
  const context = vm.createContext({
    isMediaRole: role => ["tts", "stt", "video"].includes(role),
    defaultFollowChatRoles: ["vision", "intent", "image"],
    roleForm: { value: roles }, modelRouteEditor: { value: null }, modelRouteEditorError: { value: "" },
    modelRouteSearch: { value: "" }, modelRouteStatusFilter: { value: "all" },
    llmChannels: { value: [{ id: "p1", name: "主提供商" }, { id: "p2", name: "后备提供商" }] },
    FOLLOW_CHAT: "__follow_chat__", FOLLOW_VISION: "__follow_vision__", GROUP_PREFIX: "group:", MODEL_PAIR_SEP: "::",
    selectedRoleProfiles: (_, route) => ["p1", "p2"].includes(route.profile_id) || route.group === "shared" ? [{}] : [],
    profileCanRouteRoleModel: (_, __, model) => model === "m1" || model === "m2",
    modelOptionsFor: (_, route) => [{ value: route.profile_id === "p2" ? "m2" : "m1" }]
  });
  for (const name of ["effectiveModelRoutingStrategy", "roleSnapshot", "modelRoleRoutes", "modelRouteIsStandby", "modelRouteProviderLabel", "filteredModelRoutes", "clearModelRouteFilters", "openModelRouteEditor", "setModelRouteEditorChannel", "setModelRouteEditorModel", "applyModelRouteEditor"]) {
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
  roles.chat.routing_strategy = "failover";
  context.modelRouteStatusFilter.value = "standby";
  assert.equal(context.filteredModelRoutes("chat").length, 2);
});

test("the recent-failure filter lists routes that failed, not routes that are merely unconfigured", () => {
  const roles = { chat: { profile_id: "p1", model: "m1", fallbacks: [{ profile_id: "missing", model: "m1" }, { profile_id: "p2", model: "" }, { profile_id: "p2", model: "m2" }] } };
  const context = editorContext(roles);
  Object.assign(context, {
    modelHealth: { value: { checked_at: "", candidates: [{ profile_id: "p2", model: "m2", failure_count: 2, last_failure_at: "2026-01-01T00:00:00Z" }] } },
    modelHealthReceivedAt: { value: 0 },
    selectedRoleProfiles: (_, route) => ["p1", "p2"].includes(route.profile_id) ? [{ id: route.profile_id }] : []
  });
  for (const name of ["modelHealthNow", "modelRouteCandidateHealth", "modelRouteHealth"]) {
    const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
    assert.ok(fn, name);
    vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  }
  assert.equal(context.modelRouteHealth("chat", roles.chat.fallbacks[0]).label, "未就绪");
  assert.equal(context.modelRouteHealth("chat", roles.chat.fallbacks[1]).label, "未就绪");
  context.modelRouteStatusFilter.value = "degraded";
  assert.deepEqual(Array.from(context.filteredModelRoutes("chat"), item => item.index), [3]);
  context.modelRouteStatusFilter.value = "cooling";
  assert.equal(context.filteredModelRoutes("chat").length, 0);
});

test("timeout refresh preserves edits and reads the currently edited bot only", () => {
  const context = editorContext({ chat: { model: "m1" } });
  Object.assign(context, {
    form: { value: { id: "bot-a", model_request_timeout_ms: 10000 } },
    modelRequestTimeoutDraft: { value: 10 }, savedModelRequestTimeoutMS: { value: 10000 },
    modelRequestTimeoutSeconds,
    savedRoleSnapshot: { value: context.roleSnapshot({ chat: { model: "m1" } }) }
  });
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === "syncModelRequestTimeoutWhileEditing");
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  const update = milliseconds => context.syncModelRequestTimeoutWhileEditing({ id: "bot-b", model_request_timeout_ms: 90000, profiles: [{ id: "bot-a", model_request_timeout_ms: milliseconds, model_roles: { chat: { model: "m1" } } }] });
  update(20000);
  assert.equal(context.modelRequestTimeoutDraft.value, 20);
  context.modelRequestTimeoutDraft.value = 35;
  update(25000);
  assert.equal(context.modelRequestTimeoutDraft.value, 35);
  assert.equal(context.savedModelRequestTimeoutMS.value, 25000);
  context.modelRequestTimeoutDraft.value = "";
  update(30000);
  assert.equal(context.modelRequestTimeoutDraft.value, "");
});

test("timeout stays in the bot draft across purpose changes and resets on cancel or bot switch", () => {
  assert.match(source, /v-if="role.key === 'chat'"[\s\S]*?id="bot-model-request-timeout" v-model.number="modelRequestTimeoutDraft"/);
  assert.match(source, /function setForm\(config[\s\S]*?modelRequestTimeoutDraft.value = modelRequestTimeoutSeconds\(config.model_request_timeout_ms\)/);
  assert.match(source, /function leaveEditor\([\s\S]*?setForm\(current\)/);
  assert.match(source, /function editProfile\([\s\S]*?setForm\(profile\)/);
  assert.match(source, /model_request_timeout_ms: modelRequestTimeoutMS/);
  assert.match(source, /latestTimeoutDraft !== submittedTimeoutDraft/);
  assert.match(source, /form.value !== current \|\| page.value !== "edit"/);
  const help = source.match(/<span id="bot-model-request-timeout-hint"[\s\S]*?<\/span>/)?.[0];
  for (const text of ["0 表示不额外限制", "包含流式接收", "提供商更短的超时", "调用方取消", "已发送或提交", "独立图片生成"]) assert.ok(help.includes(text));
  assert.doesNotMatch(help, /整轮总|整轮预算|180 秒/);
});

function schedulingContext(roles) {
  const context = editorContext(roles);
  Object.assign(context, { GitBranch: null, RotateCcw: null, SlidersHorizontal: null,
    isMediaRole: role => ["tts", "stt", "video"].includes(role),
    orderedRoleKeys: Object.keys, savedRoleSnapshot: { value: "" }, modelRolesChangedElsewhere: { value: false } });
  const declaration = ast.statements.find(node => ts.isVariableStatement(node) && node.declarationList.declarations.some(item => item.name.getText(ast) === "modelRoutingStrategies"));
  vm.runInContext(ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  for (const name of ["modelRoutingStrategiesFor", "setRoleRoutingStrategy", "setRoleForm", "modelRoutingDescription"]) {
    const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
    vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  }
  return context;
}

test("smart is the effective default and explicit failover round-trips", () => {
  const context = schedulingContext({ chat: { model: "m1" } });
  assert.deepEqual(Array.from(context.modelRoutingStrategiesFor("chat"), option => option.value), ["smart", "failover", "round_robin", "weighted"]);
  context.setRoleForm({ chat: { model: "m1" } });
  assert.equal(context.roleForm.value.chat.routing_strategy, undefined);
  assert.equal(context.effectiveModelRoutingStrategy("chat"), "smart");
  context.roleForm.value.chat.routing_strategy = "";
  assert.equal(context.effectiveModelRoutingStrategy("chat"), "smart");
  context.setRoleRoutingStrategy("chat", "smart");
  assert.equal(context.roleForm.value.chat.routing_strategy, "smart");
  context.setRoleForm(JSON.parse(JSON.stringify(context.roleForm.value)));
  assert.equal(context.roleForm.value.chat.routing_strategy, "smart");
  context.setRoleRoutingStrategy("chat", "failover");
  context.setRoleForm(JSON.parse(JSON.stringify(context.roleForm.value)));
  assert.equal(context.roleForm.value.chat.routing_strategy, "failover");
  assert.equal(context.effectiveModelRoutingStrategy("chat"), "failover");
  context.setRoleRoutingStrategy("vision", "smart");
  assert.equal(context.roleForm.value.vision.routing_strategy, undefined);
  assert.equal(context.roleForm.value.vision.follow_chat, true);
});

test("unsupported media has no smart option and smart retains explicit standby", () => {
  const context = schedulingContext({ chat: { model: "m1", routing_strategy: "smart", fallbacks: [{ model: "m2" }, { model: "m2", standby: true }] }, image: { model: "m1" } });
  for (const role of ["image", "tts", "stt", "video"]) assert.equal(context.modelRoutingStrategiesFor(role).some(option => option.value === "smart"), false);
  context.setRoleRoutingStrategy("image", "smart");
  assert.equal(context.roleForm.value.image.routing_strategy, undefined);
  for (const role of ["image", "tts", "stt", "video"]) assert.equal(context.effectiveModelRoutingStrategy(role), "failover");
  assert.equal(context.modelRouteIsStandby("chat", 1), false);
  assert.equal(context.modelRouteIsStandby("chat", 2), true);
  assert.match(context.modelRoutingDescription("chat"), /同分/);
  assert.match(source, /v-if="roleForm\[role.key\]\?\.routing_strategy === 'weighted'"/);
  assert.doesNotMatch(source, /SearchRoutingPanel|model-purpose-search|current.web_search/);
  assert.match(source, /const payload[\s\S]*?\.\.\.current|const saved[\s\S]*?\.\.\.current|\.\.\.current,/);
});
