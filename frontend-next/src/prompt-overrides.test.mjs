import test from "node:test";
import assert from "node:assert/strict";
import { customizedPromptCount, missingPromptVars, promptOverrideValue, withPromptOverride, withoutPromptOverrides } from "./prompt-overrides.ts";

const spec = { key: "reply.group_sender", group: "reply_base", title: "群聊发言者", usage: "", default: "正在和你说话的是「{sender}」。", vars: [{ name: "sender", description: "" }] };

test("editor shows the default until the prompt is overridden", () => {
  assert.equal(promptOverrideValue(spec, undefined), spec.default);
  assert.equal(promptOverrideValue(spec, { [spec.key]: "  " }), spec.default);
  assert.equal(promptOverrideValue(spec, { [spec.key]: "改过的" }), "改过的");
});

test("typing the default back removes the override instead of freezing a copy", () => {
  const changed = withPromptOverride(undefined, spec, "改过的\r\n第二行");
  assert.deepEqual(changed, { [spec.key]: "改过的\n第二行" });
  assert.equal(withPromptOverride(changed, spec, `${spec.default}\n`), undefined);
  assert.equal(withPromptOverride(changed, spec, ""), undefined);
  assert.deepEqual(withPromptOverride({ other: "x", [spec.key]: "y" }, spec, spec.default), { other: "x" });
});

test("dropped placeholders are reported", () => {
  assert.deepEqual(missingPromptVars(spec, undefined), []);
  assert.deepEqual(missingPromptVars(spec, { [spec.key]: "不提发言者" }), ["sender"]);
});

test("only registered keys count as customized", () => {
  assert.equal(customizedPromptCount([spec], { [spec.key]: "改", "removed.key": "旧" }), 1);
  assert.deepEqual(withoutPromptOverrides({ a: "1", b: "2" }, ["a"]), { b: "2" });
  assert.equal(withoutPromptOverrides({ a: "1" }, ["a"]), undefined);
});
