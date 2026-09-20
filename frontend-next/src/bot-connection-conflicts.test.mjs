import assert from "node:assert/strict";
import { test } from "node:test";
import { connectionGroupMembers, findWebSocketConnectionConflict, groupRoutingOverlaps, openScopeMembers, websocketConnectionKey } from "./bot-connection-conflicts.ts";

const source = { id: "source", name: "主机器人", platform: "onebot-v11", onebot_transport: "forward_ws", onebot_ws_endpoint: "ws://HOST:80", enabled: false };

test("same WebSocket warns even with disabled source or different credentials", () => {
  for (const endpoint of ["ws://host", " ws://host/ ", "ws://HOST:80/#ignored"]) {
    const draft = { ...source, id: undefined, onebot_ws_endpoint: endpoint, onebot_access_token: "different" };
    assert.equal(findWebSocketConnectionConflict(draft, [source]), source);
  }
  const reverse = { ...source, onebot_transport: "reverse_ws", onebot_reverse_ws_endpoint: "wss://HOST:443/onebot/v11/ws" };
  assert.equal(findWebSocketConnectionConflict({ ...reverse, id: undefined, onebot_reverse_ws_endpoint: "wss://host/onebot/v11/ws" }, [reverse]), reverse);
});

test("own profile, explicit aliases, and distinct endpoints do not conflict", () => {
  assert.equal(findWebSocketConnectionConflict(source, [source]), undefined);
  assert.equal(findWebSocketConnectionConflict({ ...source, id: "child", connection_profile_id: source.id }, [source]), undefined);
  for (const endpoint of ["wss://host/", "ws://host:81/", "ws://host/events", "ws://host/?account=2", "not a url", ""]) {
    assert.equal(findWebSocketConnectionConflict({ ...source, id: undefined, onebot_ws_endpoint: endpoint }, [source]), undefined);
  }
  for (const overrides of [{ platform: "telegram" }, { onebot_transport: "http" }, { onebot_transport: "reverse_ws", onebot_reverse_ws_endpoint: source.onebot_ws_endpoint }]) {
    assert.equal(findWebSocketConnectionConflict({ ...source, id: undefined, ...overrides }, [source]), undefined);
  }
  assert.equal(websocketConnectionKey({ ...source, onebot_ws_endpoint: "ws://[::1]:80" }), websocketConnectionKey({ ...source, onebot_ws_endpoint: "ws://[::1]/" }));
});

const bot = (id, name, extra = {}) => ({ id, name, enabled: true, platform: "onebot-v11", ...extra });
const whitelist = (...groups) => ({ group_admission: { mode: "whitelist", allowed_groups: groups } });

test("复用同一条连接的机器人凑成一张群归属表，只有一台时不算归属问题", () => {
  const source = bot("a", "主号", whitelist("100"));
  const reuse = bot("b", "分身", { connection_profile_id: "a", ...whitelist("200") });
  const other = bot("c", "别的连接", whitelist("100"));
  const members = connectionGroupMembers(source, [source, reuse, other]);
  assert.deepEqual(members.map((member) => member.name), ["分身", "主号"]);
  assert.deepEqual(groupRoutingOverlaps(members), []);
  assert.deepEqual(connectionGroupMembers(other, [source, reuse, other]), []);
});

test("两台白名单放行同一个群时指名到群，停用的那台不算", () => {
  const source = bot("a", "主号", whitelist("100", "300"));
  const reuse = bot("b", "分身", { connection_profile_id: "a", ...whitelist("100") });
  const off = bot("c", "停用的", { connection_profile_id: "a", enabled: false, ...whitelist("100") });
  const overlaps = groupRoutingOverlaps(connectionGroupMembers(source, [source, reuse, off]));
  assert.deepEqual(overlaps.map((overlap) => overlap.groupID), ["100"]);
  assert.deepEqual(overlaps[0].members.map((member) => member.name), ["分身", "主号"]);
});

test("不限群的机器人和任何白名单撞在一起，禁用群不算", () => {
  const source = bot("a", "主号", { disabled_groups: ["300"] });
  const reuse = bot("b", "分身", { connection_profile_id: "a", ...whitelist("100", "300") });
  const members = connectionGroupMembers(source, [source, reuse]);
  assert.deepEqual(groupRoutingOverlaps(members).map((overlap) => overlap.groupID), ["100"]);
  assert.deepEqual(openScopeMembers(members).map((member) => member.name), ["主号"]);
});

test("手上还没保存的改动顶替已保存的那一份", () => {
  const source = bot("a", "主号", whitelist("100"));
  const reuse = bot("b", "分身", { connection_profile_id: "a", ...whitelist("200") });
  const draft = { ...reuse, ...whitelist("100") };
  const overlaps = groupRoutingOverlaps(connectionGroupMembers(draft, [source, reuse]));
  assert.deepEqual(overlaps.map((overlap) => overlap.groupID), ["100"]);
  assert.equal(overlaps[0].members.some((member) => member.editing), true);
});

test("两台都不限群时只说这件事，不拿白名单里的残留群号点名", () => {
  const source = bot("a", "主号");
  const reuse = bot("b", "分身", { connection_profile_id: "a", disabled_groups: [], group_admission: { mode: "blacklist", allowed_groups: ["100"] } });
  const members = connectionGroupMembers(source, [source, reuse]);
  assert.deepEqual(groupRoutingOverlaps(members), []);
  assert.deepEqual(openScopeMembers(members).map((member) => member.name), ["分身", "主号"]);
});
