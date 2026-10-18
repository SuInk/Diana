import assert from "node:assert/strict";
import test from "node:test";
import { rssReplyRequirements, rssSourceLabel, rssSources, subscriptionPlatformLabel } from "./rss-display.ts";

test("RSS sources show names without hiding account IDs or URLs", () => {
  assert.equal(rssSourceLabel({ source: "twitter", name: "Tibo", handle: "tibo", feed_url: "https://example.com/feed" }), "X · Tibo（@tibo）");
  assert.equal(rssSourceLabel({ source: "twitter", handle: "@tibo", feed_url: "https://example.com/feed" }), "X · @tibo");
  assert.equal(rssSourceLabel({ source: "rss", name: "Diana Release", feed_url: "https://example.com/feed" }), "RSS · Diana Release（https://example.com/feed）");
  assert.equal(subscriptionPlatformLabel("telegram"), "Telegram");
});

test("multiple sources and legacy records retain their identities", () => {
  const sources = [{ name: "A", feed_url: "https://example.com/a" }, { name: "B", feed_url: "https://example.com/b" }];
  assert.deepEqual(rssSources({ feed_sources: sources }), sources);
  assert.deepEqual(rssSources({ feed_url: "https://example.com/feed", feed_source: "twitter", feed_handle: "tibo" }), [{ feed_url: "https://example.com/feed", source: "twitter", handle: "tibo" }]);
});

test("reply requirements preserve explicit empty and migrate old modes", () => {
  assert.equal(rssReplyRequirements({feed_reply_prompt:"",feed_reply_mode:"translated"}), "");
  assert.equal(rssReplyRequirements({feed_reply_prompt:"用日语总结"}), "用日语总结");
  assert.match(rssReplyRequirements({feed_reply_mode:"translated"}), /简体中文/);
  assert.equal(rssReplyRequirements({feed_rule_mode:"llm",feed_judge_prompt:"总结发布内容"}), "总结发布内容");
  assert.equal(rssReplyRequirements({feed_reply_mode:"original",feed_rule_mode:"llm"}), "");
  assert.equal(rssReplyRequirements({}), "");
});
