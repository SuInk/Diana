import test from "node:test";
import assert from "node:assert/strict";
import { roomActivity, selectRoomState } from "./room-activity.ts";
const now = Date.now();
const state = (extra = {}) => ({ profile_id: "one", epoch: "boot", revision: 1, enabled: true, pose: "rest", source: "idle", phase: "idle", active_tasks: 0, updated_at: new Date(now).toISOString(), ...extra });
const input = (room = state(), extra = {}) => ({ profile: "one", enabled: true, connected: true, now, status: { running: true, channel: { profile_id: "one", connected: true }, rooms: [room] }, ...extra });
test("runtime snapshot drives activity, not messages", () => {
  const room = state({ pose: "desk", source: "runtime", phase: "tool", active_tasks: 1, task_id: "run", expires_at: new Date(now + 1000).toISOString() });
  assert.equal(roomActivity(input(room)).pose, "desk");
  assert.equal(roomActivity(input(room)).source, "runtime");
  assert.equal(roomActivity(input(state(), { events: [{ decision: "pending" }] })).pose, "rest");
});
test("snapshot selection isolates profiles, uses revisions and accepts new server epoch", () => {
  assert.equal(selectRoomState("one", state({ revision: 3 }), state()).revision, 3);
  assert.equal(selectRoomState("one", state({ revision: 3 }), state({ epoch: "new" })).epoch, "new");
  assert.equal(selectRoomState("two", state(), state()), undefined);
});
test("missing snapshot, connection and runtime do not invent work", () => {
  for (const extra of [{ profile: "two" }, { profile: "" }, { enabled: false }, { connected: false }, { status: null }, { status: { running: false } }]) {
    assert.equal(roomActivity(input(state(), extra)).source, "unavailable");
  }
  const data = input(); data.status.rooms = [];
  assert.equal(roomActivity(data).source, "unavailable");
});
test("leases expire without requiring another SSE message", () => {
  for (const expires_at of [undefined, "bad", new Date(now).toISOString()]) {
    assert.equal(roomActivity(input(state({ active_tasks: 1, task_id: "run", expires_at }))).source, "unavailable");
  }
});
test("agent intent and terminal states stay distinct from message delivery", () => {
  for (const phase of ["completed", "cancelled", "error", "expired"]) {
    const result = roomActivity(input(state({ phase, source: "agent", pose: "phone" })));
    assert.equal(result.pose, "phone"); assert.match(result.detail, /不等于消息已送达/);
  }
  const data = input(); data.status.channel.connected = false;
  assert.equal(roomActivity(data).source, "idle");
});
