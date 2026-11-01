import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import { activeSectionIndex, visibleSectionHeadings } from "./section-outline.ts";

test("outline follows visible headings, excluding hidden tabs and empty titles", () => {
  const headings = [
    { textContent: "回复怎么发", getClientRects: () => [{}] },
    { textContent: "隐藏的平台配置", getClientRects: () => [] },
    { textContent: "  ", getClientRects: () => [{}] },
    { textContent: "安全与审核", getClientRects: () => [{}] }
  ];
  assert.deepEqual(visibleSectionHeadings({ querySelectorAll: () => headings }), [headings[0], headings[3]]);
});

test("active section follows the last heading above the sticky header boundary", () => {
  assert.equal(activeSectionIndex([], 160), -1);
  assert.equal(activeSectionIndex([300, 600, 900], 160), 0);
  assert.equal(activeSectionIndex([-300, 160, 600], 160), 1);
  assert.equal(activeSectionIndex([-800, -200, 165], 160), 2);
  assert.equal(activeSectionIndex([-800, -200, 180], 160), 1);
});

test("mobile outline uses the compact section selector", () => {
  const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
  assert.match(source, /<label for="bot-section-jump">跳转到分区<\/label>/);
  assert.match(source, /<select id="bot-section-jump"[^>]*:value="outlineActive"/);
  assert.ok(!source.includes("mobileOutlineOpen"));
});
