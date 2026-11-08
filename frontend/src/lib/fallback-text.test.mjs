import assert from "node:assert/strict";
import test from "node:test";
import { shownFallbackText, storedFallbackText, usingFallbackText } from "./fallback-text.ts";

test("empty value shows the effective fallback", () => {
  assert.equal(shownFallbackText("", "默认"), "默认");
  assert.equal(shownFallbackText("  \n", "默认"), "默认");
  assert.equal(shownFallbackText("自定义", "默认"), "自定义");
  assert.equal(shownFallbackText("", undefined), "");
});

test("text equal to the fallback is stored as empty", () => {
  assert.equal(storedFallbackText("默认\r\n第二行 ", "默认\n第二行"), "");
  assert.equal(storedFallbackText("改过", "默认"), "改过");
  assert.equal(storedFallbackText("", ""), "");
});

test("usingFallbackText only when a fallback exists and value is empty", () => {
  assert.equal(usingFallbackText("", "默认"), true);
  assert.equal(usingFallbackText("x", "默认"), false);
  assert.equal(usingFallbackText("", ""), false);
});
