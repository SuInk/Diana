import assert from "node:assert/strict";
import { test } from "node:test";
import { personaOwnsVoice, personaOwnedNotices } from "./persona-owned.ts";

// 判据只有档位。段头匹配那条路已经删掉——它要拿字符串去匹配用户写的散文，段头少
// 一个标点就悄悄改变行为，界面和运行时还可能给出不同答案。
test("只看档位，不看正文写了什么", () => {
  assert.equal(personaOwnsVoice("own"), true);
  assert.equal(personaOwnsVoice("fill"), false);
  assert.equal(personaOwnsVoice(undefined), false);
  // 正文里写什么都不影响判断：这里压根不收正文。
  assert.equal(personaOwnedNotices("fill", { selfReference: "本喵" }).length, 0);
});

// 控件藏起来之后它存的值还在配置里：藏掉一个填过「本喵」的输入框，那个值既看不见
// 也改不掉，只在某天切回填空题时突然复活。汇总必须把它点出来。
test("汇总列出接管的四项，并点出还存着的值", () => {
  assert.deepEqual(
    personaOwnedNotices("own", { selfReference: "本喵", sentenceEnders: "", actionDescriptionEnabled: true, daypartToneEnabled: false }),
    [
      { label: "自称", staleValue: "本喵" },
      { label: "句尾语气词", staleValue: "" },
      { label: "动作描写", staleValue: "已开启" },
      { label: "语气跟随时段", staleValue: "" }
    ]
  );
});
