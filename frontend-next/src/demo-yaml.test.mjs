import test from "node:test";
import assert from "node:assert/strict";
import { parseYAML, toYAML } from "./demo-yaml.ts";

test("demo YAML round-trips a persona with block text", () => {
  const persona = {
    name: "猫娘",
    system_prompt: "第一行\n第二行\n\n  缩进的第四行",
    self_reference: "咱",
    action_description_enabled: false,
    soul: { identity: "身份", values: [{ value: "诚实", why: "因为" }], honesty: ["不编"], hard_limits: [] },
    prompts: { "reply.image_only": "看图: 吐槽 # 一句", "reply.group_sender": "正在和你说话的是「{sender}」" }
  };
  const text = toYAML(persona, "头注释", { "reply.image_only": "只发图片时的正文" });
  assert.match(text, /^# 头注释\n/);
  assert.match(text, /system_prompt: \|-\n  第一行/);
  assert.match(text, /# 只发图片时的正文\n  reply.image_only: /);
  assert.doesNotMatch(text, /^\{/m);
  assert.deepEqual(parseYAML(text), persona);
});

test("demo YAML parses hand-written scalars and reports bad lines", () => {
  assert.deepEqual(parseYAML("a: 'it''s'\nb: 12\nc: true\nd: ~\ne: [] \nf: 纯文本 # 注释\n"), { a: "it's", b: 12, c: true, d: null, e: [], f: "纯文本" });
  assert.throws(() => parseYAML("a: 1\na: 2\n"), /重复/);
  assert.throws(() => parseYAML("a: 1\n  b: 2\n"), /第 2 行/);
});

test("prompt bodies render as literal blocks even on one line", () => {
  const text = toYAML({ prompts: { "reply.image_only": "看图: 一句 # 注释" } }, "", {}, new Set(["reply.image_only"]));
  assert.match(text, /reply\.image_only: \|-\n    看图: 一句 # 注释/);
  assert.deepEqual(parseYAML(text), { prompts: { "reply.image_only": "看图: 一句 # 注释" } });
});
