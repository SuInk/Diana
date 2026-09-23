import test from "node:test";
import assert from "node:assert/strict";
import { applyPersonaDocument, asCustomPersona, currentPersonaSelection, selectPersona, applyPersonaSettings, personaFromSettings, unusedPersonaName } from "./persona-settings.ts";

test("personas bundle expression settings without runtime configuration", () => {
  const current = { system_prompt: "  Diana 已写好的自定义正文\n保留原文。", action_description_enabled: true, daypart_tone_enabled: true, self_reference: "咱", sentence_enders: "呀", participation: { desire: 87 }, marked_bot_ids: ["123"] };
  const saved = personaFromSettings(current, "Diana");
  assert.equal(saved.system_prompt, current.system_prompt);
  assert.equal("reply_style" in saved, false);
  assert.equal(saved.daypart_tone_enabled, true);
  assert.equal(saved.action_description_enabled, true);
  assert.equal("participation" in saved, false);
  assert.equal("marked_bot_ids" in saved, false);
  const applied = applyPersonaSettings(current, { id: "p", name: "其它", system_prompt: "预设正文", daypart_tone_enabled: false });
  assert.equal(applied.system_prompt, current.system_prompt);
  assert.equal("reply_style" in applied, false);
  assert.equal(applied.daypart_tone_enabled, false);
  assert.deepEqual(applied.participation, current.participation);
  assert.deepEqual(applied.marked_bot_ids, current.marked_bot_ids);
});

test("prompt replacement is explicit and old personas preserve missing daypart setting", () => {
  const current = { system_prompt: "自定义正文", daypart_tone_enabled: true };
  const full = { id: "p", name: "预设", system_prompt: "已写好的人设" };
  assert.equal(applyPersonaSettings(current, full, true).system_prompt, full.system_prompt);
  assert.equal(applyPersonaSettings(current, { id: "s", name: "仅风格" }, true).system_prompt, current.system_prompt);
  assert.equal(applyPersonaSettings(current, full).daypart_tone_enabled, true);
  assert.equal(applyPersonaSettings({ system_prompt: "" }, full).system_prompt, full.system_prompt);
});

test("saving with an existing name creates a distinct copy", () => {
  assert.equal(unusedPersonaName("Diana", [{ name: "Diana" }, { name: "Diana（副本）" }]), "Diana（副本 2）");
  const name = "人".repeat(40);
  const copy = unusedPersonaName(name, [{ name }]);
  assert.notEqual(copy, name);
  assert.ok(Array.from(copy).length <= 40);
});

test("legacy settings remain custom and survive preset selection and serialization", () => {
  const legacy = { name: "Diana", system_prompt: "原来的自定义正文", daypart_tone_enabled: true, participation: { desire: 87 } };
  const preset = { id: "cat", name: "猫娘", system_prompt: "写好的人设提示词", daypart_tone_enabled: false };
  assert.equal(currentPersonaSelection(legacy, [preset]), "custom");
  const applied = selectPersona(legacy, preset);
  assert.equal(currentPersonaSelection(applied, [preset]), "cat");
  const restored = selectPersona(JSON.parse(JSON.stringify(applied)));
  assert.equal(restored.system_prompt, legacy.system_prompt);
  assert.equal(restored.daypart_tone_enabled, true);
  assert.equal(restored.name, "Diana");
  assert.deepEqual(restored.participation, legacy.participation);
  const edited = { ...applied, sentence_enders: "自定义语气" };
  assert.equal(currentPersonaSelection(edited, [preset]), "custom");
  assert.equal(asCustomPersona(edited).custom_persona.sentence_enders, "自定义语气");
  assert.equal(currentPersonaSelection(applied, []), "custom");
  assert.equal(applied.system_prompt, preset.system_prompt);
});

// 档位跟着正文一起存、一起套。一份接管模式的人设，正文里靠段头声明自己接管了
// 哪几段；套到填空题档上，段头不生效而运行时照旧注入，同一件事说两遍——而用户
// 只是从人设库里点了一下。
test("人设库存取带着人设模式", () => {
  const own = { system_prompt: "自称与语气词：平时用「我」。", persona_mode: "own", self_reference: "", sentence_enders: "" };
  const captured = personaFromSettings(own, "接管人设");
  assert.equal(captured.persona_mode, "own");

  const applied = applyPersonaSettings({ system_prompt: "" }, { id: "x", name: "接管人设", ...own }, true);
  assert.equal(applied.persona_mode, "own");
  assert.equal(applied.system_prompt, own.system_prompt);

  // 没写档位的人设（存量库里全是这种）一律按填空题套用，行为不变。
  const legacy = applyPersonaSettings({ system_prompt: "", persona_mode: "own" }, { id: "y", name: "旧人设", system_prompt: "你是一只猫娘。" }, true);
  assert.equal(legacy.persona_mode, "fill");
});

test("prompt overrides travel with the persona", () => {
  const bot = { name: "bot", system_prompt: "喵", prompt_overrides: { "reply.wake_only": "叫我就接着说" } };
  assert.deepEqual(personaFromSettings(bot, "猫娘").prompts, { "reply.wake_only": "叫我就接着说" });
  const applied = applyPersonaSettings(bot, { id: "p", name: "另一套", system_prompt: "汪" }, true);
  assert.equal(applied.prompt_overrides, undefined, "a persona without prompts means all defaults");
  const withPrompts = applyPersonaSettings(bot, { id: "p", name: "另一套", prompts: { "reply.image_only": "看图" } }, true);
  assert.deepEqual(withPrompts.prompt_overrides, { "reply.image_only": "看图" });
});

test("editing prompts turns a library persona into a custom one", () => {
  const preset = { id: "p", name: "猫娘", system_prompt: "喵", prompts: { "reply.wake_only": "叫我就接着说" } };
  const bot = { ...applyPersonaSettings({ name: "bot" }, preset, true), persona_id: "p" };
  assert.equal(currentPersonaSelection(bot, [preset]), "p");
  assert.equal(currentPersonaSelection({ ...bot, prompt_overrides: { "reply.wake_only": "改了" } }, [preset]), "custom");
  assert.equal(currentPersonaSelection({ ...bot, prompt_overrides: undefined }, [preset]), "custom");
});

test("a YAML document replaces the whole persona, soul included", () => {
  const bot = { name: "bot", system_prompt: "旧", soul: { identity: "旧品格" }, prompt_overrides: { "reply.wake_only": "旧" } };
  const next = applyPersonaDocument(bot, { id: "", name: "新", system_prompt: "新正文" });
  assert.equal(next.system_prompt, "新正文");
  assert.equal(next.soul, undefined);
  assert.equal(next.prompt_overrides, undefined);
  assert.equal(next.persona_id, "");
});
