import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

for (const view of ["AssistantView", "GroupsView"]) {
  test(`${view} separates layout, message count and forward thresholds`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.match(source, /允许多条发送/);
    assert.match(source, /reply_preserve_line_breaks/);
    assert.match(source, /换行不分条/);
    assert.match(source, /填 4 表示至少 5 条/);
    assert.doesNotMatch(source, /自然分条超过|>自然分条</);
  });
  test(`${view} shows merged-forward settings only for OneBot`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    const prefix = view === "AssistantView" ? "bot" : "group";
    const condition = view === "AssistantView" ? "isOneBotPlatform" : "supportsGroupLevel";
    for (const suffix of ["len", "chunks"]) {
      assert.match(source, new RegExp(`<div v-if="${condition}" class="field">\\s*<label for="${prefix}-forward-${suffix}">`));
    }
  });
  test(`${view} labels the reply limit per message`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.match(source, /单条回复上限（字符）/);
    assert.doesNotMatch(source, /单次回复上限（字符）/);
  });
  test(`${view} removes legacy limits and saves empty forward thresholds per scope`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /最多分几条|分段发送长度/);
    for (const field of ["forward_reply_threshold", "forward_reply_chunk_threshold"]) {
      if (view === "AssistantView") {
        assert.ok(source.includes(`${field}: Number(current.${field}) || 0`));
        assert.match(source, new RegExp(`v-model.number="[^\"]+\\.${field}"[^>]+placeholder="无上限"`));
      } else {
        // 群里留空要跟随机器人，不能存成 0 把机器人的阈值挡在群外。
        assert.ok(source.includes(`${field}: optionalForwardThreshold(current.${field})`));
        assert.match(source, new RegExp(`v-model.number="[^\"]+\\.${field}"[^>]+:placeholder="forwardThresholdPlaceholder\\('${field}'`));
      }
    }
  });
}
