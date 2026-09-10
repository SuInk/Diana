import test from "node:test";
import assert from "node:assert/strict";
import { personaLint } from "./persona-lint.ts";

const codes = warnings => warnings.map(warning => warning.code);

test("ordinary persona text stays quiet", () => {
  // 提示只在「这条已经有开关管了」时出现。写角色本身的句子一句都不该报——
  // 一个见谁都响的检查，用户第二天就学会无视它了。
  for (const text of [
    "你是 Diana：一只猫娘",
    "说话轻软，偶尔带点猫气",
    "一个爱吐槽但很靠谱的技术群管理员，遇到不懂的会直说不懂。",
    "",
    "   "
  ]) {
    assert.deepEqual(personaLint(text, { actionDescriptionEnabled: false }), [], text);
  }
});

test("mandatory sentence endings warn about the control that already handles them", () => {
  const warnings = personaLint("每句话都以「喵」收尾且不加句号");
  assert.equal(warnings.length, 2);
  assert.deepEqual(codes(warnings), ["sentence-enders", "sentence-enders"]);
  assert.equal(warnings[0].match, "每句话都以「喵」收尾");
  assert.equal(warnings[1].match, "不加句号");
  assert.notEqual(warnings[0].message, warnings[1].message);
  for (const text of ["句句都带喵", "每句都要带上语气词", "句末不打标点"]) {
    assert.deepEqual(codes(personaLint(text)), ["sentence-enders"], text);
  }
});

test("mandatory self reference points at the self reference field", () => {
  assert.deepEqual(codes(personaLint("必须自称本喵")), ["self-reference"]);
  assert.deepEqual(codes(personaLint("每句都自称咱")), ["self-reference"]);
  assert.deepEqual(personaLint("平时用「我」就好"), []);
});

test("inline actions warn only while the action toggle is off", () => {
  const text = "（歪头看你）我在的。";
  assert.deepEqual(codes(personaLint(text, { actionDescriptionEnabled: false })), ["action-description"]);
  assert.deepEqual(personaLint(text, { actionDescriptionEnabled: true }), []);
  // 开关状态没传进来时不猜：宁可漏报，也不要对着一个开着的开关喊。
  assert.deepEqual(personaLint(text), []);
  assert.deepEqual(codes(personaLint("*摇尾巴*", { actionDescriptionEnabled: false })), ["action-description"]);
});

test("formatting and length rules point at the reply controls", () => {
  for (const text of ["回复不要 Markdown", "只发纯文本", "回答要分条列出", "每次不超过 50 字", "多用 emoji", "别输出 <dianabr>"]) {
    assert.deepEqual(codes(personaLint(text)), ["formatting"], text);
  }
});

test("warnings carry the matched snippet and never mutate the input", () => {
  const options = { actionDescriptionEnabled: false, selfReference: "咱", sentenceEnders: "喵" };
  const snapshot = JSON.stringify(options);
  const warnings = personaLint("必须自称本喵，每句话都用喵结尾，（甩尾巴），回复不要 Markdown", options);
  assert.deepEqual(codes(warnings).sort(), ["action-description", "formatting", "self-reference", "sentence-enders"]);
  for (const warning of warnings) {
    assert.ok(warning.match.length > 0);
    assert.ok(warning.message.length > 0);
  }
  assert.equal(JSON.stringify(options), snapshot);
  // 纯函数：同一段正文跑两遍结果一样（正则不带 g，不会留着 lastIndex 漂移）。
  assert.deepEqual(personaLint("每句话都用喵结尾"), personaLint("每句话都用喵结尾"));
});
