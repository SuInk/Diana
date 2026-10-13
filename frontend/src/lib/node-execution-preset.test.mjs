import test from "node:test";
import assert from "node:assert/strict";
import { allowNodeExecution } from "./node-execution-preset.ts";

test("Node execution adds only node and preserves other entries and wildcard", () => {
  assert.equal(allowNodeExecution(""), "node");
  assert.equal(allowNodeExecution("uptime,*,npm,sh,uptime"), "uptime,*,npm,sh,uptime,node");
  assert.equal(allowNodeExecution(" node ,df,node,*,node,npm "), "node,df,*,npm");
});

test("Node execution is idempotent and keeps distinct executable names", () => {
  const draft = "nodejs,Node,df,node,node";
  const merged = allowNodeExecution(draft);
  assert.equal(merged, "nodejs,Node,df,node");
  assert.equal(allowNodeExecution(merged), merged);
});
