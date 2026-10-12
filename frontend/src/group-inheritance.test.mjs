import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("./views/GroupsView.vue", import.meta.url), "utf8");

// 群配置留空跟随机器人：编辑和新建时不能再把机器人的值抄进群里，否则一保存就成了快照，
// 之后机器人页改了也进不了这个群。
test("GroupsView does not copy bot values into group configs", () => {
  assert.doesNotMatch(source, /\?\?= default/);
  assert.doesNotMatch(source, /recall_reply_auto_delete_enabled: default/);
  assert.doesNotMatch(source, /social_reply_enabled: default/);
  assert.match(source, /inheritance_migrated: true/);
});

test("GroupsView offers follow-the-bot switches and placeholders", () => {
  for (const id of ["group-welcome-enabled", "group-recall-delete"]) {
    assert.match(source, new RegExp(`id="${id}"`));
  }
  assert.match(source, /跟随机器人（\$\{inherited \? "开启" : "关闭"\}）/);
  for (const field of ["group_triggers", "welcome_message", "recent_context_limit", "max_reply_chars", "max_context_tokens", "recent_history_token_budget"]) {
    assert.match(source, new RegExp(`inheritedPlaceholder\\(inheritedBot\\?\\.${field}`));
  }
});

// 数字框清空后是空串，Go 端解析整数会整份拒收；统一转成 0（跟随机器人）。
test("GroupsView coerces cleared numeric inputs", () => {
  for (const field of ["recent_history_token_budget", "recent_context_limit", "max_context_tokens", "max_reply_chars", "welcome_llm_cooldown_seconds"]) {
    assert.ok(source.includes(`${field}: followNumber(current.${field})`), field);
  }
});
