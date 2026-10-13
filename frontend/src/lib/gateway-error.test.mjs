// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import assert from "node:assert/strict";
import test from "node:test";
import { describeServerFailure, gatewayErrorPage } from "./gateway-error.ts";

const cloudflarePage = `<!DOCTYPE html>
<html class="no-js" lang="en-US"><head><title>earlyso.com | 502: Bad gateway</title></head><body>${"x".repeat(3000)}</body></html>`;

test("代理错误页被认出来，toast 指向运行记录而不是只剩状态码", () => {
  const message = describeServerFailure(502, cloudflarePage);
  assert.match(message, /^后端出错（HTTP 502）：/);
  assert.match(message, /earlyso\.com \| 502: Bad gateway/);
  assert.match(message, /运行记录/);
  assert.doesNotMatch(message, /<!DOCTYPE/i);
});

test("空正文也说明去哪看原因", () => {
  assert.match(describeServerFailure(504, ""), /^后端出错（HTTP 504）：没有收到错误说明.*运行记录/);
});

test("纯文本正文不加猜测", () => {
  assert.equal(describeServerFailure(500, "boom"), "后端出错（HTTP 500）");
  assert.equal(gatewayErrorPage("boom"), "");
});
