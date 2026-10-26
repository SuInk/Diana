import assert from "node:assert/strict";
import test from "node:test";
import { randomRepeatDefaults } from "./random-repeat.ts";

test("random repeat starts disabled and preserves explicit zero settings", () => {
  assert.deepEqual(randomRepeatDefaults(), { enabled: false, threshold: 2, probability: 30, cooldown_seconds: -1 });
  const source = { enabled: true, threshold: 3, probability: 0, cooldown_seconds: 0 };
  const draft = randomRepeatDefaults(source);
  assert.deepEqual(draft, source);
  draft.threshold = 5;
  assert.equal(source.threshold, 3);
});

test("random repeat preserves once-per-chain cooldown without changing probability", () => {
  const source = { enabled: true, threshold: 2, probability: 30, cooldown_seconds: -1 };
  assert.deepEqual(randomRepeatDefaults(source), source);
});
