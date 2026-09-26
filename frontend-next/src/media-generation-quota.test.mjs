import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { botImageGenerationLimitsPayload, dailyLimitValue, groupImageGenerationLimitsPayload } from "./media-generation-quota.ts";

// 数字框清空后是空串，Go 端解析整数会整份拒收；统一转成非负整数，0 表示不限或跟随机器人。
test("daily image limits are saved as non-negative integers", () => {
  assert.equal(dailyLimitValue(""), 0);
  assert.equal(dailyLimitValue(undefined), 0);
  assert.equal(dailyLimitValue(-3), 0);
  assert.equal(dailyLimitValue("2.6"), 3);
  assert.equal(dailyLimitValue("abc"), 0);
  assert.deepEqual(botImageGenerationLimitsPayload({ image_generation_daily_group_limit: 20, image_generation_daily_user_limit: "" }), {
    image_generation_daily_group_limit: 20,
    image_generation_daily_user_limit: 0
  });
});

// 每人上限跨群合计，只在机器人上设；群配置只带每群上限。
test("group payload only carries the per-group limit", () => {
  assert.deepEqual(groupImageGenerationLimitsPayload({ image_generation_daily_group_limit: "5", image_generation_daily_user_limit: 9 }), {
    image_generation_daily_group_limit: 5
  });
});

// 回读：后端 0 值省略字段，表单拿到 undefined 显示「留空」占位；保存时再转回 0。
test("limits read back from the backend round-trip through the payload", () => {
  const saved = JSON.parse(JSON.stringify(botImageGenerationLimitsPayload({ image_generation_daily_group_limit: 12, image_generation_daily_user_limit: 3 })));
  assert.deepEqual(botImageGenerationLimitsPayload(saved), saved);
  assert.deepEqual(botImageGenerationLimitsPayload({}), { image_generation_daily_group_limit: 0, image_generation_daily_user_limit: 0 });
});

test("AssistantView renders and saves both daily image limits", async () => {
  const source = await readFile(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  for (const field of ["image_generation_daily_group_limit", "image_generation_daily_user_limit"]) {
    assert.match(source, new RegExp(`v-model.number="form\\.${field}"[^>]+min="0"`));
  }
  assert.match(source, /\.\.\.botImageGenerationLimitsPayload\(current\)/);
});

test("GroupsView overrides the per-group image limit and follows the bot when empty", async () => {
  const source = await readFile(new URL("./views/GroupsView.vue", import.meta.url), "utf8");
  assert.match(source, /v-model.number="editing\.image_generation_daily_group_limit"/);
  assert.match(source, /inheritedPlaceholder\(inheritedBot\?\.image_generation_daily_group_limit/);
  assert.match(source, /\.\.\.groupImageGenerationLimitsPayload\(current\)/);
  assert.doesNotMatch(source, /editing\.image_generation_daily_user_limit/);
});

test("demo data carries the image limits", async () => {
  const source = await readFile(new URL("./demo.ts", import.meta.url), "utf8");
  assert.match(source, /image_generation_daily_group_limit: 30, image_generation_daily_user_limit: 5/);
  assert.match(source, /model_call_quota: 400, image_generation_daily_group_limit: 50/);
});
