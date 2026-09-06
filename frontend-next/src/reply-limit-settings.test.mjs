import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

for (const view of ["AssistantView", "GroupsView"]) {
  test(`${view} removes legacy limits and saves empty forward thresholds as zero`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /最多分几条|分段发送长度/);
    for (const field of ["forward_reply_threshold", "forward_reply_chunk_threshold"]) {
      assert.ok(source.includes(`${field}: Number(current.${field}) || 0`));
      assert.match(source, new RegExp(`v-model.number="[^\"]+\\.${field}"[^>]+placeholder="无上限"`));
    }
  });
}
