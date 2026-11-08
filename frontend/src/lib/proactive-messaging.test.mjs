import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { PROACTIVE_DEFAULTS, parseProactiveGroupIDs, proactiveFormValue, proactivePayload } from "./proactive-messaging.ts";

test("没配过的主动开口按后端默认值填表，默认关闭", () => {
  const value = proactiveFormValue(undefined);
  assert.equal(value.enabled, false);
  assert.equal(value.owner_enabled, false);
  assert.deepEqual(value.group_ids, []);
  assert.equal(value.group_idle_minutes, PROACTIVE_DEFAULTS.group_idle_minutes);
  assert.equal(value.owner_idle_hours, 8);
  assert.equal(value.daily_limit, 2);
  assert.equal(value.quiet_start, "23:00");
  assert.equal(value.quiet_end, "08:00");
});

test("群号支持换行、逗号和空格分隔，并去重", () => {
  assert.deepEqual(parseProactiveGroupIDs("10001\n10002, 10003，10001  \n\n"), ["10001", "10002", "10003"]);
});

test("保存时清空的数字框按 0 交给后端补默认值", () => {
  const payload = proactivePayload({ enabled: true, group_idle_minutes: "", daily_limit: 3 }, "10001");
  assert.equal(payload.enabled, true);
  assert.deepEqual(payload.group_ids, ["10001"]);
  // 表单补默认值后才取整：空串回到 120，不会变成 0。
  assert.equal(payload.group_idle_minutes, 120);
  assert.equal(payload.daily_limit, 3);
});

test("主动开口卡片放在入群欢迎后面，标明试验", async () => {
  const view = await readFile(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
  const welcome = view.indexOf("<h2>入群欢迎</h2>");
  const proactive = view.indexOf("<h2>主动开口</h2>");
  assert.ok(welcome > 0 && proactive > welcome);
  assert.match(view.slice(proactive, proactive + 400), /试验/);
  assert.match(view, /proactive_messaging: proactivePayload\(current\.proactive_messaging, proactiveGroupsDraft\.value\)/);
});
