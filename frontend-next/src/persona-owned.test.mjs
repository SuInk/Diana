import assert from "node:assert/strict";
import { test } from "node:test";
import { personaOwnedHeaders, personaOwnsField } from "./persona-owned.ts";

test("段头出现即视为接管，判据和后端一致", () => {
  assert.equal(personaOwnsField("自称与语气词：平时用「我」。", "voice"), true);
  assert.equal(personaOwnsField("动作描写：不写括号动作。", "action"), true);
  // 段头出现在正文中间也算：后端用的是子串包含，不是行首匹配。
  assert.equal(personaOwnsField("性格：随和。\n自称与语气词：平时用「我」。", "voice"), true);
  assert.equal(personaOwnsField("你是一只猫娘，自称本喵。", "voice"), false);
  assert.equal(personaOwnsField("", "voice"), false);
  assert.equal(personaOwnsField(undefined, "action"), false);
});

// 段头和后端 personaOwnedSections 逐字对齐，对不上就会出现「界面说已接管、运行时
// 照旧注入」这种两头不认账的状态。这份清单写死在这里当锁。
test("段头清单不能随手改", () => {
  assert.deepEqual(personaOwnedHeaders, { voice: "自称与语气词：", action: "动作描写：" });
});
