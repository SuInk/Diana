import test from "node:test";
import assert from "node:assert/strict";
import { effectiveReplyTone, FALLBACK_REPLY_TONE, normalizeReplyTone, replyToneOverride } from "./reply-tone.ts";

test("reply tone editor shows the built-in text for an empty override", () => {
  assert.equal(effectiveReplyTone("", "内置口吻"), "内置口吻");
  assert.equal(effectiveReplyTone(undefined, undefined), FALLBACK_REPLY_TONE);
});

test("unchanged built-in stays an empty override while edits are saved", () => {
  assert.equal(replyToneOverride("\r\n 内置口吻 \r\n", "内置口吻"), "");
  assert.equal(replyToneOverride(" 自定义口吻\r\n第二行 ", "内置口吻"), "自定义口吻\n第二行");
  assert.equal(normalizeReplyTone(" 一\r\n二 "), "一\n二");
});
