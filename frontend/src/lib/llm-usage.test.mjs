import assert from "node:assert/strict";
import test from "node:test";
import { cacheUsageDisplay, cacheUtilization, groupLLMUsage, usageModelKey, usagePurposeLabel, usageShare } from "./llm-usage.ts";

const entry = (purpose, provider, model, input, output, calls = 1, missing = 0) => ({
  purpose, provider, model, input_tokens: input, output_tokens: output,
  total_tokens: input + output, cached_input_tokens: Math.floor(input / 2),
  recorded_calls: calls, missing_usage_calls: missing
});
const entries = [
  entry("reply", "a", "shared-model", 100, 20, 2, 1),
  entry("memory_extract", "a", "shared-model", 10, 5),
  entry("reply", "b", "shared-model", 8, 2),
  entry("reply", "a", "other-model", 2, 1),
  entry("", "", "", 0, 0, 1, 1)
];

test("用途和模型分类都与原始计数相加一致，缓存不重复计入总量", () => {
  const fields = ["recorded_calls", "input_tokens", "output_tokens", "cached_input_tokens", "total_tokens", "missing_usage_calls"];
  for (const dimension of ["purpose", "model"]) {
    const groups = groupLLMUsage(entries, dimension);
    for (const field of fields) {
      assert.equal(groups.reduce((sum, group) => sum + group[field], 0), entries.reduce((sum, item) => sum + item[field], 0));
    }
  }
  const purposes = groupLLMUsage(entries, "purpose");
  assert.deepEqual(purposes.map((group) => [group.label, group.total_tokens]), [["聊天回复", 133], ["记忆抽取", 15], ["未标注用途", 0]]);
  assert.equal(purposes[0].recorded_calls, 4);
  assert.equal(purposes[0].missing_usage_calls, 1);
});

test("同名模型按提供商分开，可按用途或模型交叉筛选", () => {
  const models = groupLLMUsage(entries, "model");
  assert.equal(models.length, 4);
  assert.equal(models[0].label, "shared-model");
  assert.equal(models[0].detail, "a");
  assert.equal(models[0].total_tokens, 135);
  const replyModels = groupLLMUsage(entries, "model", "reply");
  assert.deepEqual(replyModels.map((group) => group.total_tokens), [120, 10, 3]);
  const modelPurposes = groupLLMUsage(entries, "purpose", usageModelKey(entries[0]));
  assert.deepEqual(modelPurposes.map((group) => group.total_tokens), [120, 15]);
  assert.deepEqual(groupLLMUsage(entries, "model", "nonexistent"), []);
});

test("未报用量和历史空字段照样计数，新增用途原样显示", () => {
  const groups = groupLLMUsage([entry(" ", " ", " ", 0, 0, 2, 2)], "model");
  assert.equal(groups[0].label, "未知模型");
  assert.equal(groups[0].recorded_calls, 2);
  assert.equal(groups[0].missing_usage_calls, 2);
  assert.equal(usagePurposeLabel(" future_purpose "), "future_purpose");
  assert.equal(usagePurposeLabel("constructor"), "constructor");
  assert.equal(usagePurposeLabel("webui_provider_test"), "控制台提供商测试");
  assert.deepEqual(groupLLMUsage([], "purpose"), []);
});

test("占比使用筛选后的总量，零分母及小份额有明确显示", () => {
  assert.equal(usageShare(120, 135), "88.9%");
  assert.equal(usageShare(1, 100000), "<0.1%");
  assert.equal(usageShare(0, 100), "0.0%");
  assert.equal(usageShare(0, 0), "—");
});

test("缓存利用率用输入 Token 做分母，并可分别查看筛选结果", () => {
  assert.equal(cacheUtilization(50, 100), "50.0%");
  assert.equal(cacheUtilization(445308, 770760), "57.8%");
  assert.equal(cacheUtilization(1, 100000), "<0.1%");
  assert.equal(cacheUtilization(0, 100), "0.0%");
  assert.equal(cacheUtilization(0, 0), "—");
  assert.equal(cacheUsageDisplay(742180, 1284600), "742,180 Token（57.8%）");
  assert.equal(cacheUsageDisplay(0, 0), "0 Token（—）");
  // 总利用率按输入量加权，不把每个模型的百分比直接平均。
  const models = groupLLMUsage([entry("reply", "a", "large", 900, 0), {
    ...entry("reply", "a", "small", 100, 0), cached_input_tokens: 100
  }], "model");
  assert.equal(cacheUsageDisplay(
    models.reduce((sum, model) => sum + model.cached_input_tokens, 0),
    models.reduce((sum, model) => sum + model.input_tokens, 0)
  ), "550 Token（55.0%）");
});
