import assert from "node:assert/strict";
import { test } from "node:test";
import { builtinPersonas, withBuiltinPersonas, isBuiltinPersona } from "./builtin-personas.ts";
import { selectPersona, currentPersonaSelection } from "./persona-settings.ts";

test("built-in personas remain available alongside saved personas", () => {
  const saved = { id: "saved", name: "我的人设", system_prompt: "原有人设" };
  const library = withBuiltinPersonas([saved]);
  assert.deepEqual(library.slice(0, 5).map(p => p.name), ["猫娘", "真人感", "助手", "女友", "男友"]);
  assert.equal(library[5], saved);
  assert.equal(isBuiltinPersona(saved), false);
  library[0].system_prompt = "edited copy";
  assert.notEqual(withBuiltinPersonas([])[0].system_prompt, "edited copy");
});

test("each built-in applies real settings and preserves the custom persona", () => {
  const original = { enabled: false, onebot_reverse_ws_endpoint: "", system_prompt: "我自己的角色", self_reference: "我", sentence_enders: "呀", action_description_enabled: false, daypart_tone_enabled: false };
  for (const preset of builtinPersonas) {
    const applied = selectPersona(original, preset);
    assert.equal(applied.system_prompt, preset.system_prompt);
    assert.equal(currentPersonaSelection(applied, withBuiltinPersonas([])), preset.id);
    const restored = selectPersona(applied);
    assert.equal(restored.system_prompt, original.system_prompt);
    assert.equal(restored.sentence_enders, original.sentence_enders);
  }
});
