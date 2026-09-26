// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import assert from "node:assert/strict";
import test from "node:test";
import { describeLLMTestError, randomTestModel, testModelIDs } from "./llm-test.ts";

const profile = {
  provider: "gemini",
  model: "default-model",
  models: [{ id: "default-model" }, { id: "second-model" }, { id: "third-model" }]
};

test("model test chooses across the deduplicated configured model list", () => {
  assert.deepEqual(testModelIDs(profile), ["default-model", "second-model", "third-model"]);
  assert.equal(randomTestModel(profile, () => 0), "default-model");
  assert.equal(randomTestModel(profile, () => 0.5), "second-model");
  assert.equal(randomTestModel(profile, () => 0.999), "third-model");
});

test("model test explains a body-less gateway failure", () => {
  const message = describeLLMTestError(new Error("后端出错（HTTP 502）"), "Gemini", "third-model");
  assert.match(message, /Gemini · third-model/);
  assert.match(message, /网关没有返回模型错误正文/);
  assert.match(message, /llm_test/);
});

test("model test displays the exact 502 response body", () => {
  const error = Object.assign(new Error("后端出错（HTTP 502）"), {
    responseBody: JSON.stringify({ error: { message: "upstream model unavailable", code: "bad_gateway" } })
  });
  const message = describeLLMTestError(error, "Gemini", "third-model");
  assert.equal(message, "Gemini · third-model 测试失败：upstream model unavailable（后端出错（HTTP 502））");
});

test("model test falls back to the raw body when it has no message field", () => {
  const error = Object.assign(new Error("后端出错（HTTP 502）"), { responseBody: "upstream reset the connection" });
  const message = describeLLMTestError(error, "Gemini", "third-model");
  assert.match(message, /响应正文：\nupstream reset the connection/);
});

test("model test keeps the upstream reason when available", () => {
  assert.equal(
    describeLLMTestError(new Error("provider returned 429: quota exceeded"), "Gemini", "second-model"),
    "Gemini · second-model 测试失败：provider returned 429: quota exceeded"
  );
});

test("网关 HTML 错误页只留一句出处，不整段贴出来", () => {
  const body = `<!DOCTYPE html>
<html class="no-js" lang="en-US"><head><title>earlyso.com | 502: Bad gateway</title>
<meta charset="UTF-8" /></head><body>${"x".repeat(5000)}</body></html>`;
  const error = Object.assign(new Error("后端出错（HTTP 502）"), { responseBody: body });
  const message = describeLLMTestError(error, "TypeSafe 判断模型", "jev-latest");
  assert.match(message, /来自反向代理或网关/);
  assert.match(message, /earlyso\.com \| 502: Bad gateway/);
  assert.doesNotMatch(message, /<!DOCTYPE/i);
  assert.ok(message.length < 300, `错误文案不该被 HTML 撑爆：${message.length}`);
});

test("非 HTML 的纯文本正文仍然原样展示", () => {
  const error = Object.assign(new Error("后端出错（HTTP 502）"), { responseBody: "upstream reset the connection" });
  assert.match(describeLLMTestError(error, "Gemini", "x"), /响应正文：\nupstream reset the connection/);
});

test("api.ts 补过去向说明的 5xx 不会和测试框的说明叠两遍", () => {
  const error = Object.assign(new Error("后端出错（HTTP 502）：后端的错误说明被反向代理或网关换成了它自己的错误页（x），原始原因请到「运行记录」查看。"), {
    status: 502,
    responseBody: "<html><title>x</title></html>"
  });
  const message = describeLLMTestError(error, "Gemini", "m");
  assert.equal((message.match(/运行记录/g) ?? []).length, 1, message);
  assert.match(message, /后端出错（HTTP 502）。这段响应来自反向代理或网关（x）/);
});
