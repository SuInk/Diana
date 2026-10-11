import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

test("automatic sticker order stays beside reply controls with explicit options and scope", async () => {
  const source = await readFile(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  const section = source.slice(source.indexOf("<h2>回复怎么发</h2>"), source.indexOf("<!-- 准入与限额"));
  assert.match(section, /<AppSelect\s+id="bot-sticker-reply-order"/);
  assert.match(section, /:model-value="form\.sticker_reply_order \?\? 'before'"/);
  assert.match(section, /:options="stickerReplyOrderOptions"/);
  assert.match(section, /form\.sticker_reply_order = value as 'before' \| 'after' \| 'auto'/);
  assert.ok(section.indexOf('id="bot-sticker-reply-order"') > section.indexOf('id="bot-mention-user-mode"'));
  assert.match(section, /仅控制自动配图与最终正文的发送顺序，不改变是否配图/);
  assert.match(section, /Telegram 流式草稿可能提前显示/);
  assert.match(section, /超时或预算不足会跳过配图，优先发送正文/);
  const options = source.match(/const stickerReplyOrderOptions: AppSelectOption\[\] = (\[[\s\S]*?\]);/);
  assert.ok(options);
  assert.deepEqual(JSON.parse(JSON.stringify(vm.runInNewContext(options[1]))), [
    { value: "before", label: "先发表情（默认）" },
    { value: "after", label: "后发表情" },
    { value: "auto", label: "让模型决定" },
  ]);
});

test("sticker order defaults legacy forms to before and preserves all explicit choices", async () => {
  const source = await readFile(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  const setForm = source.slice(source.indexOf("function setForm(config:"));
  const expression = setForm.match(/sticker_reply_order:\s*([^,\n]+)/)?.[1];
  assert.ok(expression);
  for (const value of [undefined, "before", "after", "auto"]) {
    assert.equal(vm.runInNewContext(expression, { config: { sticker_reply_order: value } }), value ?? "before");
  }
});

test("demo profiles and new bot defaults start with stickers before text", async () => {
  const source = await readFile(new URL("./demo.ts", import.meta.url), "utf8");
  const profile = source.slice(source.indexOf("const oneBotProfile:"), source.indexOf("const telegramProfile:"));
  const defaults = source.slice(source.indexOf('if (path === "/api/assistant/config/defaults"'), source.indexOf('if (path === "/api/assistant/config"'));
  for (const block of [profile, defaults]) assert.match(block, /sticker_reply_order: "before"/);
  assert.match(source, /const telegramProfile:[\s\S]*?\.\.\.oneBotProfile/);
});

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
      assert.match(source, new RegExp(`<div v-if="${condition} && [^"]+" class="field">\\s*<label for="${prefix}-forward-${suffix}">`));
    }
  });
  test(`${view} labels the reply limit per message`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.match(source, /单条回复上限（字符）/);
    assert.doesNotMatch(source, /单次回复上限（字符）/);
  });
  test(`${view} removes legacy limits and turns forward cards off explicitly`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /最多分几条|分段发送长度/);
    // 关闭合并转发要有显式开关，不能再让人往字数里填 0。
    assert.doesNotMatch(source, /0 或留空关闭|填 0 本群关闭/);
    for (const field of ["forward_reply_threshold", "forward_reply_chunk_threshold"]) {
      assert.match(source, new RegExp(`v-model.number="[^\"]+\\.${field}"[^>]+min="1"`));
    }
    if (view === "AssistantView") {
      assert.match(source, /v-model="form.forward_reply_enabled"/);
      for (const field of ["forward_reply_threshold", "forward_reply_chunk_threshold"]) {
        assert.ok(source.includes(`${field}: Number(current.${field}) || 0`));
      }
    } else {
      // 群里三选一；留空的阈值跟随机器人，不能存成 0 把机器人的阈值挡在群外。
      assert.match(source, /id="group-forward-mode"/);
      assert.match(source, /label: "本群关闭"/);
      for (const field of ["forward_reply_threshold", "forward_reply_chunk_threshold"]) {
        assert.ok(source.includes(`optionalForwardThreshold(current.${field})`));
      }
    }
  });
}
