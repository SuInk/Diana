import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { participationFromConfig, participationPreset, participationPresetName, participationLevelOptions, changeParticipationLevel, changeParticipationThresholdLevel, participationThresholdLevel, participationThresholdOptions, participationLevelLabel, participationLevelThresholds } from "./participation.ts";

test("reply desire offers only four named levels", () => {
  assert.deepEqual(participationLevelOptions.map(option => option.label), ["低", "中", "高", "极高"]);
  const legacy = { desire: 83, social: 67, followup: 22, restraint: 12, information: 35, cooldown_seconds: 45 };
  assert.deepEqual(participationFromConfig({ participation: legacy }), legacy);
});

test("legacy preferences map to named levels using desire only", () => {
  for (const name of ["off", "low", "medium", "high", "max"]) {
    assert.equal(participationPresetName(participationPreset(name)), name);
  }
  const custom = { ...participationPreset("max"), information: 17 };
  assert.equal(participationPresetName(custom), "max");
  assert.equal(participationPreset("max").information, 0);
});

test("cooldown defaults to 30 seconds independently of desire", () => {
  for (const level of ["off", "low", "medium", "high", "max"]) {
    assert.equal(participationPreset(level).cooldown_seconds,30);
    assert.equal(participationFromConfig({chat_in_level:level}).cooldown_seconds,30);
  }
  assert.equal(participationFromConfig({chat_in_level:"max",chat_in_cooldown_seconds:45}).cooldown_seconds,45);
  assert.equal(participationFromConfig({participation:{...participationPreset("max"),cooldown_seconds:undefined}}).cooldown_seconds,30);
  assert.equal(participationFromConfig({participation:{...participationPreset("max"),cooldown_seconds:0}}).cooldown_seconds,0);
});

test("switching desire retains cooldown and changing cooldown retains the preset name", () => {
  for (const seconds of [0, 30, 45, 600]) {
    for (const level of ["off", "low", "medium", "high", "max"]) {
      const next = participationPreset(level, seconds);
      assert.equal(next.cooldown_seconds,seconds);
      assert.equal(participationPresetName(next),level);
    }
  }
});

test("score thresholds have four levels and remain independent of desire and cooldown", () => {
  assert.deepEqual(participationThresholdOptions.map(option => [option.label, option.score]), [["低", 40], ["中", 60], ["高", 80], ["极高", 90]]);
  const initial = { ...participationPreset("low", 120), relevance_threshold: 80, substance_threshold: 90 };
  for (const level of ["low", "medium", "high", "max"]) {
    const desire = changeParticipationLevel(initial, level);
    assert.equal(desire.relevance_threshold, 80);
    assert.equal(desire.substance_threshold, 90);
    assert.equal(desire.cooldown_seconds, 120);
    const threshold = changeParticipationThresholdLevel(initial, "substance_threshold", level);
    assert.equal(participationThresholdLevel(threshold.substance_threshold), level);
    assert.equal(threshold.relevance_threshold, 80);
    assert.equal(threshold.desire, 25);
    assert.equal(threshold.cooldown_seconds, 120);
    assert.deepEqual(participationFromConfig({ participation: JSON.parse(JSON.stringify(threshold)) }), threshold);
  }
  assert.equal(participationThresholdLevel(), "medium");
  assert.equal(participationThresholdLevel(65), "custom");
  const legacy = { ...initial, substance_threshold: 65 };
  assert.equal(changeParticipationLevel(legacy, "high").substance_threshold, 65);
});

test("每个参与度档位都标出后端的评分门槛", () => {
  assert.deepEqual(["off", "minimal", "low", "medium", "high", "extreme", "always"].map(level => participationLevelLabel(level)), [
    "关（不判断）",
    "极低（≥0.90，最严）",
    "低（≥0.70）",
    "中（≥0.50）",
    "高（≥0.30）",
    "极高（≥0.10，最松）",
    "总是（不看分数）",
  ]);
  // 摘要里省掉最严/最松，只留门槛本身。
  assert.deepEqual(["off", "minimal", "medium", "extreme", "always"].map(level => participationLevelLabel(level, { compact: true })), ["关 不判断", "极低 ≥0.90", "中 ≥0.50", "极高 ≥0.10", "总是 不看分数"]);
  // 选择器里用各自的文案，门槛照样跟着走。
  assert.equal(participationLevelLabel("medium", { label: "适中" }), "适中（≥0.50）");
  assert.equal(participationLevelLabel("always", { label: "完全不限制" }), "完全不限制（不看分数）");
});

test("门槛数字逐项对得上 ratingPasses", () => {
  const source = readFileSync(new URL("../../model/assistant/participation_single_score.go", import.meta.url), "utf8");
  const literal = source.match(/map\[string\]float64\{([^}]*)\}/);
  assert.ok(literal, "ratingPasses 里应当有 map[string]float64 门槛表");
  const backend = Object.fromEntries([...literal[1].matchAll(/"(\w+)":\s*([0-9.]+)/g)].map(([, level, score]) => [level, Number(score)]));
  assert.deepEqual(backend, { minimal: 0.9, low: 0.7, medium: 0.5, high: 0.3, extreme: 0.1 });
  for (const [level, threshold] of Object.entries(backend)) assert.equal(participationLevelThresholds[level], threshold);
  // off 和 always 在 ratingPasses 里提前返回，前端用 null 表示「不走门槛」。
  assert.equal(participationLevelThresholds.off, null);
  assert.equal(participationLevelThresholds.always, null);
  assert.deepEqual(Object.keys(participationLevelThresholds), ["off", ...Object.keys(backend), "always"]);
});
