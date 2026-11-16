import assert from "node:assert/strict";
import test from "node:test";
import { createLiveEventSync, hasUnlistedNewest, isSameLiveEvent } from "./live-events.ts";

const listed = { id: "e1", message_id: "30001", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-a" };

function harness(listedEvents) {
  const timers = [];
  const refreshed = [];
  let checks = 0;
  const sync = createLiveEventSync({
    delayMs: 800,
    isListed: ref => listedEvents.some(event => isSameLiveEvent(event, ref)),
    refresh: ref => refreshed.push(ref.message_id),
    checkForNew: () => { checks++; },
    setTimer: (callback, ms) => { const handle = { callback, ms, cleared: false }; timers.push(handle); return handle; },
    clearTimer: handle => { handle.cleared = true; }
  });
  const flush = () => { for (const timer of timers.splice(0)) if (!timer.cleared) timer.callback(); };
  return { sync, flush, refreshed, checks: () => checks };
}

test("same event needs message, kind and session to match; a missing profile on either side is not compared", () => {
  assert.equal(isSameLiveEvent(listed, { message_id: " 30001 ", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-a" }), true);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "group", group_id: "10001", user_id: "20001" }), true);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-b" }), false);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "private", user_id: "20001" }), false);
  assert.equal(isSameLiveEvent(listed, { kind: "group", group_id: "10001" }), false);
});

test("updates for a listed event refresh it in place and never light the new-events button", () => {
  const { sync, flush, refreshed, checks } = harness([listed]);
  // 结果推送和中途几十步调用链进度：合并成一次重拉，不去核实新事件。
  sync.push({ ...listed });
  for (let step = 0; step < 5; step++) sync.push({ message_id: "30001", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-a" });
  flush();
  assert.deepEqual(refreshed, ["30001"]);
  assert.equal(checks(), 0);
});

test("two listed events updated at the same moment both refresh", () => {
  const other = { ...listed, id: "e2", message_id: "30002" };
  const { sync, flush, refreshed } = harness([listed, other]);
  sync.push({ ...listed });
  sync.push({ ...other });
  flush();
  assert.deepEqual(refreshed.sort(), ["30001", "30002"]);
});

test("unlisted events only trigger one verification; records without a message id are ignored", () => {
  const { sync, flush, refreshed, checks } = harness([listed]);
  sync.push({ kind: "notice", group_id: "10001" });
  sync.push({ message_id: "30009", kind: "group", group_id: "10001", user_id: "20009" });
  sync.push({ message_id: "30010", kind: "group", group_id: "10002", user_id: "20010" });
  flush();
  assert.equal(checks(), 1);
  assert.deepEqual(refreshed, []);
});

test("dispose cancels pending refreshes and checks", () => {
  const { sync, flush, refreshed, checks } = harness([listed]);
  sync.push({ ...listed });
  sync.push({ message_id: "30009", kind: "group", group_id: "10001" });
  sync.dispose();
  flush();
  assert.deepEqual(refreshed, []);
  assert.equal(checks(), 0);
});

test("new events exist only when the newest under current filters is not already listed", () => {
  assert.equal(hasUnlistedNewest(undefined, ["e1"]), false);
  assert.equal(hasUnlistedNewest({ id: "e1" }, ["e1", "e2"]), false);
  assert.equal(hasUnlistedNewest({ id: "e3" }, ["e1", "e2"]), true);
  assert.equal(hasUnlistedNewest({ id: "e3" }, []), true);
});
