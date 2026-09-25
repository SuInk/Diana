import assert from "node:assert/strict";
import test from "node:test";
import { matchingPersona, soulFileName, soulLength, soulTitle, unusedPersonaName } from "./soul-library.ts";

test("soulTitle reads the first-line heading only", () => {
  assert.equal(soulTitle("\n# 嘉然\n\n## 概述"), "嘉然");
  assert.equal(soulTitle("她说话很短。\n# 不是标题"), "");
  assert.equal(soulTitle("## 二级标题"), "");
  assert.equal(soulTitle(""), "");
});

test("matchingPersona ignores surrounding whitespace but nothing else", () => {
  const personas = [{ id: "a", name: "A", system_prompt: "# A\n正文" }];
  assert.equal(matchingPersona("  # A\n正文\n", personas)?.id, "a");
  assert.equal(matchingPersona("# A\n正文。", personas), undefined);
  assert.equal(matchingPersona("   ", personas), undefined);
});

test("unusedPersonaName appends copy suffixes within 40 chars", () => {
  const personas = [{ id: "1", name: "嘉然" }, { id: "2", name: "嘉然（副本）" }];
  assert.equal(unusedPersonaName("嘉然", personas), "嘉然（副本 2）");
  assert.equal(unusedPersonaName("小满", personas), "小满");
  const long = "长".repeat(50);
  assert.ok(Array.from(unusedPersonaName(long, [{ id: "x", name: "长".repeat(40) }])).length <= 40);
});

test("soulLength counts code points and soulFileName strips unsafe characters", () => {
  assert.equal(soulLength("嘉然😀"), 3);
  assert.equal(soulFileName("a/b:嘉然"), "ab嘉然.md");
  assert.equal(soulFileName("  "), "SOUL.md");
});
