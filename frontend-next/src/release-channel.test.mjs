import assert from "node:assert/strict";
import { test } from "node:test";
import { channelSwitchConfirm, releaseAllowedOnChannel } from "./release-channel.ts";

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

test("switching to a prerelease channel warns about automatic install only when it is on", () => {
  assert.match(channelSwitchConfirm("canary", true).message, /自动重启并安装/);
  assert.doesNotMatch(channelSwitchConfirm("canary", false).message, /自动重启并安装/);
  assert.equal(channelSwitchConfirm("beta", false).danger, true);
  assert.match(channelSwitchConfirm("release", true).message, /不会自动降级/);
});
