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

test("relations and word cloud requests carry the group's own bot profile", async () => {
  const pick = (name) => source.match(new RegExp(`(?:async )?function ${name}\\([\\s\\S]*?\\n}`))[0];
  const code = ["openRelations", "loadRelations", "openWordCloud", "loadWordCloud"].map(pick).join("\n");
  const calls = [];
  const ref = (value) => ({ value });
  const context = vm.createContext({
    botScope: ref(""),
    relationsGroupID: ref(""), relationsGroupName: ref(""), relationsProfileID: ref(""), relationsCenter: ref(""),
    relationsRange: ref("24h"), relationsGraph: ref(null), relationsLoading: ref(false),
    wordCloudGroupID: ref(""), wordCloudGroupName: ref(""), wordCloudProfileID: ref(""), wordCloudRange: ref("7d"),
    wordCloud: ref(null), wordCloudLoading: ref(false), toastError: (message) => { throw new Error(message); },
    getGroupRelations: async (...args) => { calls.push(["relations", ...args]); return { graph: { bot_id: "", center_id: "x" } }; },
    getGroupWordCloud: async (...args) => { calls.push(["wordcloud", ...args]); return { cloud: {} }; }
  });
  vm.runInContext(ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  // 「全部机器人」视图下 botScope 为空，只能靠群自己的 bot_profile_id 认出它是 Telegram 群。
  context.openRelations({ group_id: "-10012345", bot_profile_id: "tg-bot" });
  context.openWordCloud({ group_id: "-10012345", bot_profile_id: "tg-bot" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls, [["relations", "-10012345", "24h", "", "tg-bot"], ["wordcloud", "-10012345", "7d", "tg-bot"]]);
  // 群没记机器人时退回当前选中的机器人。
  calls.length = 0;
  context.botScope.value = "qq-bot";
  context.openRelations({ group_id: "12345" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(calls, [["relations", "12345", "24h", "", "qq-bot"]]);
});
