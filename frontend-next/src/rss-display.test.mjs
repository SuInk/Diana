import assert from "node:assert/strict";
import test from "node:test";
import { rssSourceLabel, rssSources, subscriptionPlatformLabel } from "./rss-display.ts";

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
