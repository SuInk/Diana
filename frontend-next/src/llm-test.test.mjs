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
  assert.match(message, /llm\.test/);
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
