import assert from "node:assert/strict";
import { test } from "node:test";
import { builtinPersonas, withBuiltinPersonas, isBuiltinPersona, defaultSystemPrompt } from "./builtin-personas.ts";
import { selectPersona, currentPersonaSelection } from "./persona-settings.ts";
import { personaLint } from "./persona-lint.ts";

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

// 一两句形容词的人设，模型读完只拿到一个词，落到具体一句话上仍旧是客服腔。
// 这条钉住的是密度：够长、有「说话方式」这一段、示例里有足够多句本人台词。
// 上限同样要钉——人设正文是要跟工具规则和运行时规范抢上下文的，写成长文不是优点。
test("每份内置人设都写满能模仿的密度", () => {
  for (const preset of builtinPersonas) {
    const text = preset.system_prompt ?? "";
    const runes = [...text].length;
    assert.ok(runes >= 600 && runes <= 900, `${preset.name} 长度 ${runes} 不在 600～900 之间`);
    for (const section of ["身份与来历：", "性格：", "说话方式：", "关系与称呼：", "边界：", "示例——"]) {
      assert.ok(text.includes(section), `${preset.name} 缺少「${section}」这一段`);
    }
    const replies = (text.match(/(^|\n)你：/g) ?? []).length;
    assert.ok(replies >= 6, `${preset.name} 的示例只有 ${replies} 句本人台词，至少要有 6 句`);
    // 示例是成对的：每句「你：」前面都得有一句「用户：」，不然那不是对话。
    assert.equal((text.match(/(^|\n)用户：/g) ?? []).length, replies, `${preset.name} 的示例对话不成对`);
    // 示例块在正文末尾：前面是人物本身，后面是怎么说话的样例。
    assert.ok(text.indexOf("示例——") < text.indexOf("\n你："), `${preset.name} 的示例块位置不对`);
  }
});

// 出厂预设不能踩人设编辑器自己的检查线。用户一选内置人设就看到一排黄色提示，
// 那条检查立刻变成噪音——他会先学会无视提示，再也不看真正写错的那次。
// 每份都按它自己的开关值跑：动作描写那条规则只在开关关着时才检查。
test("内置人设不触发编辑器里的写法提示", () => {
  for (const preset of builtinPersonas) {
    const warnings = personaLint(preset.system_prompt ?? "", {
      actionDescriptionEnabled: preset.action_description_enabled,
      selfReference: preset.self_reference,
      sentenceEnders: preset.sentence_enders
    });
    assert.deepEqual(warnings, [], `${preset.name} 触发了提示：${JSON.stringify(warnings)}`);
  }
});

// 开关和正文必须是一套。猫娘那份正文和示例全靠说话本身撑，一个括号动作都没有，
// 所以动作描写开关关着；关着的时候 persona-lint 才会去查正文里的括号动作，上面
// 那条测试也就顺带钉住了「以后别往猫娘正文里加括号动作」。
test("动作描写开关和正文写法一致", () => {
  const byId = Object.fromEntries(builtinPersonas.map(preset => [preset.id, preset]));
  assert.equal(byId["builtin:catgirl"].action_description_enabled, false);
  for (const preset of builtinPersonas) {
    if (preset.action_description_enabled) continue;
    assert.doesNotMatch(preset.system_prompt ?? "", /（[^）]{1,12}）|\*[^*\n]{1,10}\*/, `${preset.name} 关着动作描写却在正文里写了动作`);
  }
});

// 人设不能假设自己在哪儿说话：同一份人设存在共享人设库里，能套到任何一个机器人上，
// 同一个机器人又同时在群聊和私聊里回话，平台还可能是 QQ 以外的任何一个。写死「你被
// 养在这个群里」「群友会来问你」，人设一进私聊就成了假话，而场景和平台本来就由运行时
// 按当轮注入（promptGroupScope、群聊发言者模板、platformOutputRulesForConfig）。
test("内置人设不假设场合，也不假设平台", () => {
  const venueWords = ["群里", "群聊", "本群", "群友", "QQ", "Telegram", "飞书", "企业微信", "OneBot"];
  for (const preset of [...builtinPersonas, { name: "默认人设", system_prompt: defaultSystemPrompt }]) {
    for (const word of venueWords) {
      assert.ok(
        !(preset.system_prompt ?? "").includes(word),
        `${preset.name} 写了「${word}」：人设要能跨机器人、跨平台、跨群聊和私聊复用，场合和平台由运行时注入，正文里提到别人就写「对方」「别人」「大家」`
      );
    }
  }
});

// 前端这份 defaultSystemPrompt 和后端 model/assistant/types.go 里的常量逐字节对照，
// 放在 model/assistant/frontend_parity_test.go：那条断言要同时读 builtin-personas.ts
// 和 types.go，而这套前端测试还会在 Docker 的 frontend-next 构建阶段里跑
//（npm 的 prebuild），那一层只有 frontend-next/ 一个目录，读不到 .go 源码。
