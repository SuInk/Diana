import assert from "node:assert/strict";
import { test } from "node:test";
import { findWebSocketConnectionConflict, websocketConnectionKey } from "./bot-connection-conflicts.ts";

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
