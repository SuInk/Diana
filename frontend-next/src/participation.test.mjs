import test from "node:test";
import assert from "node:assert/strict";
import { participationFromConfig, participationPreset, participationPresetName, participationLevelOptions } from "./participation.ts";

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
