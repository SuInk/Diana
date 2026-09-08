import test from "node:test";
import assert from "node:assert/strict";
import { participationFromConfig, participationPreset, participationPresetName } from "./participation.ts";

test("preset and custom slider values round trip", () => {
  for (const name of ["off", "low", "medium", "high", "max"]) {
    assert.equal(participationPresetName(participationPreset(name)), name);
  }
  const custom = { ...participationPreset("max"), information: 17 };
  assert.equal(participationPresetName(custom), "custom");
  assert.equal(participationPreset("max").information, 0);
});

test("cooldown presets and existing zero values remain distinct", () => {
  assert.equal(participationPreset("max").cooldown_seconds,30);
  assert.equal(participationPreset("low").cooldown_seconds,600);
  assert.equal(participationFromConfig({chat_in_level:"max",chat_in_cooldown_seconds:0}).cooldown_seconds,0);
  assert.equal(participationFromConfig({chat_in_level:"max",chat_in_cooldown_seconds:45}).cooldown_seconds,45);
  assert.equal(participationPresetName({...participationPreset("max"),cooldown_seconds:0}),"custom");
});
