import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";

const source = readFileSync(new URL("../views/GroupsView.vue", import.meta.url), "utf8");
const select = source.match(/function selectRelationsCenter\([\s\S]*?\n}/)[0];
function harness() {
  let requests = 0;
  const graph = { bot_id: "bot", center_id: "bot", nodes: [
    { user_id: "bot", is_bot: true, avatar_url: "/avatars/bot" },
    { user_id: "alice", avatar_url: "/avatars/alice?v=abc" },
    { user_id: "bob", avatar_url: "/avatars/bob" }
  ], edges: [{ source: "alice", target: "bob", weight: 5 }] };
  const context = vm.createContext({ relationsGraph: { value: graph }, relationsCenter: { value: "" },
    relationsLoading: { value: false }, loadRelations: () => { requests++; } });
  vm.runInContext(ts.transpileModule(select, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return { context, graph, requests: () => requests };
}

test("changing relation center keeps nodes, avatar URLs and edges without fetching or loading", () => {
  const { context: c, graph, requests } = harness();
  for (const id of ["alice", "bob", "bot", "alice", ""]) {
    c.selectRelationsCenter(id);
    assert.equal(c.relationsGraph.value.center_id, id || "bot");
    assert.equal(c.relationsGraph.value.nodes, graph.nodes);
    assert.equal(c.relationsGraph.value.edges, graph.edges);
    assert.equal(c.relationsLoading.value, false);
  }
  assert.equal(c.relationsCenter.value, "");
  assert.equal(requests(), 0);
});

test("unknown members and pending range changes cannot replace the current center", () => {
  const { context: c, requests } = harness();
  c.selectRelationsCenter("unknown");
  assert.equal(c.relationsGraph.value.center_id, "bot");
  c.relationsLoading.value = true;
  c.selectRelationsCenter("alice");
  assert.equal(c.relationsGraph.value.center_id, "bot");
  assert.equal(requests(), 0);
});
