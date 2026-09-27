import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  botImageGenerationLimitsPayload,
  botVideoGenerationLimitsPayload,
  dailyLimitValue,
  groupDailyLimitMode,
  groupImageGenerationLimitsPayload,
  groupImageLimitMode,
  groupVideoGenerationLimitsPayload,
  setGroupDailyLimitMode,
  setGroupImageLimitMode
} from "./media-generation-quota.ts";

// 数字框清空后是空串，Go 端解析整数会整份拒收；统一转成非负整数，0 表示不限。
test("bot daily image limits are saved as non-negative integers", () => {
  assert.equal(dailyLimitValue(""), 0);
  assert.equal(dailyLimitValue(undefined), 0);
  assert.equal(dailyLimitValue(-3), 0);
  assert.equal(dailyLimitValue("2.6"), 3);
  assert.equal(dailyLimitValue("abc"), 0);
  assert.deepEqual(botImageGenerationLimitsPayload({ image_generation_daily_group_limit: 20, image_generation_daily_user_limit: "", daily_limit_timezone: " Asia/Tokyo " }), {
    image_generation_daily_group_limit: 20,
    image_generation_daily_user_limit: 0,
    daily_limit_timezone: "Asia/Tokyo"
  });
});

// 回读：后端 0 值省略字段，表单拿到 undefined 显示「留空」占位；保存时再转回 0。
test("bot limits read back from the backend round-trip through the payload", () => {
  const saved = JSON.parse(JSON.stringify(botImageGenerationLimitsPayload({ image_generation_daily_group_limit: 12, image_generation_daily_user_limit: 3, daily_limit_timezone: "UTC" })));
  assert.deepEqual(botImageGenerationLimitsPayload(saved), saved);
  assert.deepEqual(botImageGenerationLimitsPayload({}), { image_generation_daily_group_limit: 0, image_generation_daily_user_limit: 0, daily_limit_timezone: "" });
});

// 群配置三态：不带字段跟随机器人，0 本群不限，正数本群上限。存一遍、读回来还是同一态。
test("group limit distinguishes follow, unlimited and custom", () => {
  for (const [stored, mode] of [[undefined, ""], [null, ""], [0, "unlimited"], [5, "custom"], ["", "custom"]]) {
    assert.equal(groupImageLimitMode(stored), mode, String(stored));
  }
  const roundTrip = (config) => JSON.parse(JSON.stringify({ ...config, ...groupImageGenerationLimitsPayload(config) }));
  assert.equal(groupImageLimitMode(roundTrip({}).image_generation_daily_group_limit), "");
  assert.equal(roundTrip({ image_generation_daily_group_limit: 0 }).image_generation_daily_group_limit, 0);
  assert.equal(roundTrip({ image_generation_daily_group_limit: "7" }).image_generation_daily_group_limit, 7);
  // 选了单独设置却没填数，按跟随机器人保存，不能存成 0（那是「不限」）。
  assert.equal("image_generation_daily_group_limit" in roundTrip({ image_generation_daily_group_limit: "" }), false);
  // 每人上限跨群合计，群配置里不带。
  assert.equal("image_generation_daily_user_limit" in groupImageGenerationLimitsPayload({ image_generation_daily_user_limit: 9 }), false);
});

test("switching the group limit mode keeps or clears the value", () => {
  const config = { image_generation_daily_group_limit: 5 };
  setGroupImageLimitMode(config, "unlimited");
  assert.equal(config.image_generation_daily_group_limit, 0);
  setGroupImageLimitMode(config, "custom");
  assert.equal(config.image_generation_daily_group_limit, "");
  assert.equal(groupImageLimitMode(config.image_generation_daily_group_limit), "custom");
  config.image_generation_daily_group_limit = 8;
  setGroupImageLimitMode(config, "custom");
  assert.equal(config.image_generation_daily_group_limit, 8);
  setGroupImageLimitMode(config, "");
  assert.equal(config.image_generation_daily_group_limit, undefined);
});

test("AssistantView renders and saves the daily image limits and reset timezone", async () => {
  const source = await readFile(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  for (const field of ["image_generation_daily_group_limit", "image_generation_daily_user_limit"]) {
    assert.match(source, new RegExp(`v-model.number="form\\.${field}"[^>]+min="0"`));
  }
  assert.match(source, /v-model="form\.daily_limit_timezone"/);
  assert.match(source, /\.\.\.botImageGenerationLimitsPayload\(current\)/);
});

test("GroupsView offers follow, unlimited and custom for the per-group image limit", async () => {
  const source = await readFile(new URL("./views/GroupsView.vue", import.meta.url), "utf8");
  assert.match(source, /id="group-image-limit-mode"/);
  assert.match(source, /label: "本群不限"/);
  assert.match(source, /跟随机器人（\$\{inherited \? `\$\{inherited\} 次` : "不限"\}）/);
  assert.match(source, /v-if="groupDailyLimitMode\(editing\.image_generation_daily_group_limit\) === 'custom'"/);
  assert.match(source, /v-model.number="editing\.image_generation_daily_group_limit"[^>]+min="1"/);
  assert.match(source, /\.\.\.groupImageGenerationLimitsPayload\(current\)/);
  assert.doesNotMatch(source, /editing\.image_generation_daily_user_limit/);
});

test("demo data carries the image limits", async () => {
  const source = await readFile(new URL("./demo.ts", import.meta.url), "utf8");
  assert.match(source, /image_generation_daily_group_limit: 30, image_generation_daily_user_limit: 5/);
  assert.match(source, /model_call_quota: 400, image_generation_daily_group_limit: 50/);
});

// 视频和生图同一套：机器人页 0 不限，群配置三态，保存往返不变态。
test("video limits save and read back like image limits", () => {
  assert.deepEqual(botVideoGenerationLimitsPayload({ video_generation_daily_group_limit: "3", video_generation_daily_user_limit: "" }), {
    video_generation_daily_group_limit: 3,
    video_generation_daily_user_limit: 0
  });
  const saved = JSON.parse(JSON.stringify(botVideoGenerationLimitsPayload({ video_generation_daily_group_limit: 4, video_generation_daily_user_limit: 1 })));
  assert.deepEqual(botVideoGenerationLimitsPayload(saved), saved);
  const roundTrip = (config) => JSON.parse(JSON.stringify({ ...config, ...groupVideoGenerationLimitsPayload(config) }));
  assert.equal(groupDailyLimitMode(roundTrip({}).video_generation_daily_group_limit), "");
  assert.equal(groupDailyLimitMode(roundTrip({ video_generation_daily_group_limit: 0 }).video_generation_daily_group_limit), "unlimited");
  assert.equal(roundTrip({ video_generation_daily_group_limit: "2" }).video_generation_daily_group_limit, 2);
  assert.equal("video_generation_daily_group_limit" in roundTrip({ video_generation_daily_group_limit: "" }), false);
  // 切换视频的模式不碰生图那一项。
  const config = { image_generation_daily_group_limit: 7, video_generation_daily_group_limit: 3 };
  setGroupDailyLimitMode(config, "video_generation_daily_group_limit", "unlimited");
  assert.deepEqual(config, { image_generation_daily_group_limit: 7, video_generation_daily_group_limit: 0 });
  setGroupDailyLimitMode(config, "video_generation_daily_group_limit", "");
  assert.equal(config.video_generation_daily_group_limit, undefined);
  assert.equal(config.image_generation_daily_group_limit, 7);
});

test("AssistantView and GroupsView render and save the video limits", async () => {
  const assistant = await readFile(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  for (const field of ["video_generation_daily_group_limit", "video_generation_daily_user_limit"]) {
    assert.match(assistant, new RegExp(`v-model.number="form\\.${field}"[^>]+min="0"`));
  }
  assert.match(assistant, /\.\.\.botVideoGenerationLimitsPayload\(current\)/);
  const groups = await readFile(new URL("./views/GroupsView.vue", import.meta.url), "utf8");
  assert.match(groups, /id="group-video-limit-mode"/);
  assert.match(groups, /groupDailyLimitOptions\('video_generation_daily_group_limit'\)/);
  assert.match(groups, /v-if="groupDailyLimitMode\(editing\.video_generation_daily_group_limit\) === 'custom'"/);
  assert.match(groups, /v-model.number="editing\.video_generation_daily_group_limit"[^>]+min="1"/);
  assert.match(groups, /\.\.\.groupVideoGenerationLimitsPayload\(current\)/);
  assert.doesNotMatch(groups, /editing\.video_generation_daily_user_limit/);
  const demo = await readFile(new URL("./demo.ts", import.meta.url), "utf8");
  assert.match(demo, /video_generation_daily_group_limit: 5, video_generation_daily_user_limit: 1/);
  assert.match(demo, /image_generation_daily_group_limit: 50, video_generation_daily_group_limit: 0/);
});
