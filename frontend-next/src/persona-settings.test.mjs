import test from "node:test";
import assert from "node:assert/strict";
import { asCustomPersona, currentPersonaSelection, selectPersona, applyPersonaSettings, personaFromSettings, unusedPersonaName } from "./persona-settings.ts";

test("personas bundle expression settings without runtime configuration", () => {
  const current = { system_prompt: "  Diana 已写好的自定义正文\n保留原文。", reply_style: "human", action_description_enabled: true, daypart_tone_enabled: true, self_reference: "咱", sentence_enders: "呀", participation: { desire: 87 }, marked_bot_ids: ["123"] };
  const saved = personaFromSettings(current, "Diana");
  assert.equal(saved.system_prompt, current.system_prompt);
  assert.equal(saved.reply_style, "human");
  assert.equal(saved.daypart_tone_enabled, true);
  assert.equal(saved.action_description_enabled, true);
  assert.equal("participation" in saved, false);
  assert.equal("marked_bot_ids" in saved, false);
  const applied = applyPersonaSettings(current, { id: "p", name: "其它", system_prompt: "预设正文", reply_style: "catgirl", daypart_tone_enabled: false });
  assert.equal(applied.system_prompt, current.system_prompt);
  assert.equal(applied.reply_style, "catgirl");
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
  const legacy = { name: "Diana", system_prompt: "原来的自定义正文", reply_style: "human", daypart_tone_enabled: true, participation: { desire: 87 } };
  const preset = { id: "cat", name: "猫娘", system_prompt: "写好的人设提示词", reply_style: "catgirl", daypart_tone_enabled: false };
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
