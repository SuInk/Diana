// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import test from "node:test";
import assert from "node:assert/strict";
import { formatCompactNumber, formatDurationMS, niceAxisMax } from "./format.ts";

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

test("耗时按量级换单位", () => {
  assert.equal(formatDurationMS(0), "0ms");
  assert.equal(formatDurationMS(742), "742ms");
  assert.equal(formatDurationMS(1000), "1.0s");
  assert.equal(formatDurationMS(51_300), "51.3s");
  assert.equal(formatDurationMS(72_400), "1m 12s");
});

test("缺值和负数给破折号，不写成 0ms", () => {
  assert.equal(formatDurationMS(undefined), "—");
  assert.equal(formatDurationMS(null), "—");
  assert.equal(formatDurationMS(-1), "—");
});

test("坐标轴上限取到 1、2、2.5、5 档的整数，刻度是整数", () => {
  assert.equal(niceAxisMax(82), 100);
  assert.equal(niceAxisMax(41), 50);
  assert.equal(niceAxisMax(100), 100);
  assert.equal(niceAxisMax(7), 10);
  assert.equal(niceAxisMax(3), 4);
  assert.equal(niceAxisMax(1), 2);
  assert.equal(niceAxisMax(0), 2);
  assert.equal(niceAxisMax(5), 6);
  assert.equal(niceAxisMax(1234), 2000);
});
