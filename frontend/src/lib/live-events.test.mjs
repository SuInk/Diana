import assert from "node:assert/strict";
import test from "node:test";
import { createLiveEventSync, hasUnlistedNewest, isSameLiveEvent } from "./live-events.ts";

const listed = { id: "e1", message_id: "30001", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-a" };

function harness(listedEvents, expandedIDs = []) {
  const timers = [];
  const refreshed = [];
  const traces = [];
  let checks = 0;
  const sync = createLiveEventSync({
    delayMs: 800,
    isListed: ref => listedEvents.some(event => isSameLiveEvent(event, ref)),
    isExpanded: ref => listedEvents.some(event => expandedIDs.includes(event.id) && isSameLiveEvent(event, ref)),
    refresh: ref => refreshed.push(ref.message_id),
    refreshTrace: ref => traces.push(ref.message_id),
    checkForNew: () => { checks++; },
    setTimer: (callback, ms) => { const handle = { callback, ms, cleared: false }; timers.push(handle); return handle; },
    clearTimer: handle => { handle.cleared = true; }
  });
  const flush = () => { for (const timer of timers.splice(0)) if (!timer.cleared) timer.callback(); };
  return { sync, flush, refreshed, traces, checks: () => checks };
}

test("same event needs message, kind and session to match; a missing profile on either side is not compared", () => {
  assert.equal(isSameLiveEvent(listed, { message_id: " 30001 ", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-a" }), true);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "group", group_id: "10001", user_id: "20001" }), true);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "group", group_id: "10001", user_id: "20001", profile_id: "bot-b" }), false);
  assert.equal(isSameLiveEvent(listed, { message_id: "30001", kind: "private", user_id: "20001" }), false);
  assert.equal(isSameLiveEvent(listed, { kind: "group", group_id: "10001" }), false);
});

test("a result for a listed event refreshes it in place and never lights the new-events button", () => {
  const { sync, flush, refreshed, traces, checks } = harness([listed]);
  sync.push({ ...listed }, "result");
  sync.push({ ...listed }, "result");
  flush();
  assert.deepEqual(refreshed, ["30001"]);
  assert.deepEqual(traces, []);
  assert.equal(checks(), 0);
});

test("trace progress only reloads an expanded trace, merged into one reload", () => {
  const collapsed = harness([listed]);
  for (let step = 0; step < 5; step++) collapsed.sync.push({ ...listed }, "progress");
  collapsed.flush();
  assert.deepEqual(collapsed.traces, []);
  assert.deepEqual(collapsed.refreshed, []);
  assert.equal(collapsed.checks(), 0);

  const expanded = harness([listed], ["e1"]);
  for (let step = 0; step < 5; step++) expanded.sync.push({ ...listed }, "progress");
  expanded.flush();
  assert.deepEqual(expanded.traces, ["30001"]);
  assert.deepEqual(expanded.refreshed, []);
});

test("a result absorbs pending trace progress for the same event, since its refresh reloads the trace too", () => {
  const { sync, flush, refreshed, traces } = harness([listed], ["e1"]);
  sync.push({ ...listed }, "progress");
  sync.push({ ...listed }, "result");
  sync.push({ ...listed }, "progress");
  flush();
  assert.deepEqual(refreshed, ["30001"]);
  assert.deepEqual(traces, []);
});

test("two listed events updated at the same moment both refresh", () => {
  const other = { ...listed, id: "e2", message_id: "30002" };
  const { sync, flush, refreshed } = harness([listed, other]);
  sync.push({ ...listed }, "result");
  sync.push({ ...other }, "result");
  flush();
  assert.deepEqual(refreshed.sort(), ["30001", "30002"]);
});

test("unlisted results trigger one verification; progress and records without a message id never do", () => {
  const { sync, flush, refreshed, checks } = harness([listed]);
  sync.push({ kind: "notice", group_id: "10001" }, "result");
  sync.push({ message_id: "30008", kind: "group", group_id: "10001", user_id: "20008" }, "progress");
  flush();
  assert.equal(checks(), 0);
  sync.push({ message_id: "30009", kind: "group", group_id: "10001", user_id: "20009" }, "result");
  sync.push({ message_id: "30010", kind: "group", group_id: "10002", user_id: "20010" }, "result");
  flush();
  assert.equal(checks(), 1);
  assert.deepEqual(refreshed, []);
});

test("dispose cancels pending refreshes and checks", () => {
  const { sync, flush, refreshed, traces, checks } = harness([listed], ["e1"]);
  sync.push({ ...listed }, "progress");
  sync.push({ message_id: "30009", kind: "group", group_id: "10001" }, "result");
  sync.dispose();
  flush();
  assert.deepEqual(refreshed, []);
  assert.deepEqual(traces, []);
  assert.equal(checks(), 0);
});

test("new events exist only when the newest under current filters is not already listed", () => {
  assert.equal(hasUnlistedNewest(undefined, ["e1"]), false);
  assert.equal(hasUnlistedNewest({ id: "e1" }, ["e1", "e2"]), false);
  assert.equal(hasUnlistedNewest({ id: "e3" }, ["e1", "e2"]), true);
  assert.equal(hasUnlistedNewest({ id: "e3" }, []), true);
});
