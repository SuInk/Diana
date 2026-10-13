import test from "node:test";
import assert from "node:assert/strict";
import { composePromptSections, customizedPromptCount, missingPromptVars, parsePromptSections, promptOverrideValue, promptSectionsMatch, withPromptOverride, withPromptSections, withoutPromptOverrides } from "./prompt-overrides.ts";

const spec = { key: "reply.group_sender", group: "reply_base", title: "群聊发言者", usage: "", default: "正在和你说话的是「{sender}」。", vars: [{ name: "sender", description: "" }] };

test("editor shows the default until the prompt is overridden", () => {
  assert.equal(promptOverrideValue(spec, undefined), spec.default);
  assert.equal(promptOverrideValue(spec, { [spec.key]: "  " }), spec.default);
  assert.equal(promptOverrideValue(spec, { [spec.key]: "改过的" }), "改过的");
});

test("typing the default back removes the override instead of freezing a copy", () => {
  const changed = withPromptOverride(undefined, spec, "改过的\r\n第二行");
  assert.deepEqual(changed, { [spec.key]: "改过的\n第二行" });
  assert.equal(withPromptOverride(changed, spec, `${spec.default}\n`), undefined);
  assert.equal(withPromptOverride(changed, spec, ""), undefined);
  assert.deepEqual(withPromptOverride({ other: "x", [spec.key]: "y" }, spec, spec.default), { other: "x" });
});

test("dropped placeholders are reported", () => {
  assert.deepEqual(missingPromptVars(spec, undefined), []);
  assert.deepEqual(missingPromptVars(spec, { [spec.key]: "不提发言者" }), ["sender"]);
});

test("only registered keys count as customized", () => {
  assert.equal(customizedPromptCount([spec], { [spec.key]: "改", "removed.key": "旧" }), 1);
  assert.deepEqual(withoutPromptOverrides({ a: "1", b: "2" }, ["a"]), { b: "2" });
  assert.equal(withoutPromptOverrides({ a: "1" }, ["a"]), undefined);
});

const parsed = { key: "routing.x", group: "routing", title: "分类", usage: "", default: "判断关系。", contract: "\n\n只输出 JSON。", format_key: "routing.x.format" };

test("output formats are editable and reset together with the body", async () => {
  const { promptFormatValue, withPromptFormat, isPromptCustomized, isPromptFormatCustomized, withoutPromptCustomization } = await import("./prompt-overrides.ts");
  assert.equal(promptFormatValue(parsed, undefined), "只输出 JSON。");
  const changed = withPromptFormat(undefined, parsed, "只输出 {\"relation\":\"new\"}");
  assert.deepEqual(changed, { "routing.x.format": "只输出 {\"relation\":\"new\"}" });
  assert.ok(isPromptCustomized(parsed, changed) && isPromptFormatCustomized(parsed, changed));
  assert.equal(withPromptFormat(changed, parsed, "只输出 JSON。\n"), undefined);
  assert.equal(withoutPromptCustomization({ "routing.x": "改", "routing.x.format": "改" }, parsed), undefined);
});

const sectionSpecs = [
  { key: "a", group: "g", title: "接话评分 · 算作", usage: "", default: "默认甲" },
  { key: "b", group: "g", title: "接话评分 · 不算", usage: "", default: "默认乙\n第二行" }
];
const sections = sectionSpecs.map((spec) => ({ spec, title: spec.title.replace("接话评分 · ", "") }));

test("sections compose into one box and split back per key", () => {
  const text = composePromptSections(sections, { a: "改过的甲" });
  assert.equal(text, "【算作】\n改过的甲\n\n【不算】\n默认乙\n第二行");
  const parsed = parsePromptSections(text, sections);
  assert.deepEqual(parsed, { ok: true, values: { a: "改过的甲", b: "默认乙\n第二行" } });
  assert.deepEqual(withPromptSections(undefined, sections, parsed.values), { a: "改过的甲" });
  assert.equal(promptSectionsMatch(parsed.values, sections, { a: "改过的甲" }), true);
  assert.equal(promptSectionsMatch(parsed.values, sections, undefined), false);
});

test("section order is free but headings must all be there exactly once", () => {
  assert.deepEqual(parsePromptSections("【不算】\n乙\n【算作】\n甲", sections), { ok: true, values: { a: "甲", b: "乙" } });
  assert.equal(parsePromptSections("【算作】\n甲", sections).ok, false);
  assert.equal(parsePromptSections("【算作】\n甲\n【算作】\n再来\n【不算】\n乙", sections).ok, false);
  assert.equal(parsePromptSections("开头\n【算作】\n甲\n【不算】\n乙", sections).ok, false);
  assert.equal(parsePromptSections("【算作】\n\n【不算】\n乙", sections).ok, false);
  assert.equal(parsePromptSections("【算作】\n甲甲甲\n【不算】\n乙", sections, 2).ok, false);
});

test("unknown bracket lines stay inside the section body", () => {
  assert.deepEqual(parsePromptSections("【算作】\n【别的】\n甲\n【不算】\n乙", sections), { ok: true, values: { a: "【别的】\n甲", b: "乙" } });
});
