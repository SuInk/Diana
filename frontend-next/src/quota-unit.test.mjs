// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import assert from "node:assert/strict";
import { test } from "node:test";
import { formatTokenQuota, parseTokenQuota, tokenQuotaReadout } from "./quota-unit.ts";

// 裸数字按 K 算：写额度的人心里想的是「五十万」，不是五位数一个个数零。
test("bare numbers default to K", () => {
  assert.equal(parseTokenQuota("500"), 500_000);
  assert.equal(parseTokenQuota("1.5"), 1_500);
  assert.equal(parseTokenQuota(" 2 000 "), 2_000_000);
});

test("explicit units win over the default", () => {
  assert.equal(parseTokenQuota("500k"), 500_000);
  assert.equal(parseTokenQuota("1.5m"), 1_500_000);
  assert.equal(parseTokenQuota("50万"), 500_000);
  assert.equal(parseTokenQuota("50w"), 500_000);
  // 想按原始 token 数填的人也有：token / t 表示不乘。
  assert.equal(parseTokenQuota("8000token"), 8_000);
  assert.equal(parseTokenQuota("8000t"), 8_000);
});

test("empty and unparsable input are distinguishable", () => {
  assert.equal(parseTokenQuota(""), undefined);
  assert.equal(parseTokenQuota("   "), undefined);
  assert.equal(parseTokenQuota("五十万"), undefined);
  assert.equal(parseTokenQuota("500kk"), undefined);
});

// 存的是精确 token 数，回填时要还原成人写得出来的样子。
test("round-trips through the K-based draft", () => {
  assert.equal(formatTokenQuota(500_000), "500");
  assert.equal(formatTokenQuota(1_500_000), "1500");
  assert.equal(formatTokenQuota(0), "");
  assert.equal(formatTokenQuota(undefined), "");
  // 不是整千的值不能被 K 抹掉，原样带单位写出来。
  assert.equal(formatTokenQuota(8_192), "8192token");
  assert.equal(parseTokenQuota(formatTokenQuota(8_192)), 8_192);
  assert.equal(parseTokenQuota(formatTokenQuota(500_000)), 500_000);
});

// 同一个输入框在群和机器人两处的归属说法不一样，留空/0 都要说清楚跟谁走。
test("the readout carries the caller's fallback wording", () => {
  assert.match(tokenQuotaReadout("", "留空跟随机器人。"), /^留空跟随机器人。/);
  assert.match(tokenQuotaReadout("0", "留空跟随机器人。"), /留空跟随机器人。$/);
  assert.match(tokenQuotaReadout("0", "留空不限。"), /留空不限。$/);
  assert.equal(tokenQuotaReadout("500", "留空不限。"), "= 500,000 token");
  assert.match(tokenQuotaReadout("五十万", "留空不限。"), /看不懂/);
});
