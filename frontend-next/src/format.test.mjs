// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import test from "node:test";
import assert from "node:assert/strict";
import { formatCompactNumber } from "./format.ts";

test("四位数以下照原样给，不必读成 0.97K", () => {
  assert.equal(formatCompactNumber(0), "0");
  assert.equal(formatCompactNumber(7), "7");
  assert.equal(formatCompactNumber(999), "999");
});

test("按量级收进 K/M/B，小数位随整数位递减", () => {
  assert.equal(formatCompactNumber(1000), "1.00K");
  assert.equal(formatCompactNumber(12_345), "12.3K");
  assert.equal(formatCompactNumber(412_000), "412K");
  assert.equal(formatCompactNumber(1_381_020), "1.38M");
  assert.equal(formatCompactNumber(2_500_000_000), "2.50B");
});

test("缺值和非数不该把卡片打成 NaN", () => {
  assert.equal(formatCompactNumber(undefined), "0");
  assert.equal(formatCompactNumber(null), "0");
  assert.equal(formatCompactNumber(Number.NaN), "0");
});
