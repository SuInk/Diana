import assert from "node:assert/strict";
import { test } from "node:test";
import { channelSwitchConfirm, latestRollbackReleaseTags, releaseAllowedOnChannel } from "./release-channel.ts";

const release = (tag, prerelease = tag.includes("-")) => ({ tag, prerelease });

test("each channel lists only the releases it can update to", () => {
  const tags = [
    release("v0.8.128-canary.3"),
    release("v0.8.128-rc.1"),
    release("v0.8.128-beta.2"),
    release("v0.8.127"),
    release("v0.8.127-dev"),
    release("nightly-2026-09-20")
  ];
  const visible = (channel) => tags.filter((item) => releaseAllowedOnChannel(item, channel)).map((item) => item.tag);
  assert.deepEqual(visible("release"), ["v0.8.127"]);
  assert.deepEqual(visible("beta"), ["v0.8.128-rc.1", "v0.8.128-beta.2", "v0.8.127"]);
  assert.deepEqual(visible("canary"), ["v0.8.128-canary.3", "v0.8.128-rc.1", "v0.8.128-beta.2", "v0.8.127"]);
});

test("a plain version tag marked prerelease is not a stable release", () => {
  assert.equal(releaseAllowedOnChannel(release("v0.8.127", true), "canary"), false);
  assert.equal(releaseAllowedOnChannel(release("v0.8.127+build.5", false), "release"), true);
});

test("rollback uses GitHub's latest five stable releases before comparing the current version", () => {
  const tags = latestRollbackReleaseTags([
    release("v1.4.0-canary.1"),
    release("v1.4.0-beta.1"),
    release("v1.3.0"),
    release("v1.2.0"),
    release("v1.1.0"),
    release("v1.0.0-rc.1", false),
    release(""),
    release("v1.0.0"),
    release("v0.9.0"),
    release("v0.8.0"),
    release("v0.7.0")
  ]);
  assert.deepEqual([...tags], ["v1.3.0", "v1.2.0", "v1.1.0", "v1.0.0", "v0.9.0"]);
  // 当前运行 v1.1.0 时，更旧版本只有列表里的 v1.0.0 / v0.9.0；不能补入第 6 个。
  assert.equal(tags.has("v0.8.0"), false);
  assert.equal(tags.has("v0.7.0"), false);
});

test("switching to a prerelease channel warns about automatic install only when it is on", () => {
  assert.match(channelSwitchConfirm("canary", true).message, /自动安装并重启/);
  assert.doesNotMatch(channelSwitchConfirm("canary", false).message, /自动安装并重启/);
  assert.equal(channelSwitchConfirm("beta", false).danger, true);
  assert.match(channelSwitchConfirm("release", true).message, /不会自动降级/);
});
