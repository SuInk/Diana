import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { NodeTypes } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";

test("bot settings remove context merging while retaining explicit sharing features", () => {
  const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  const { descriptor, errors } = parse(source);
  assert.deepEqual(errors, []);
  const models = [];
  let relayManager = false;
  const visit = node => {
    if (node.type === NodeTypes.ELEMENT) {
      if (node.tag === "MessageRelayManager") relayManager = true;
      for (const prop of node.props) {
        if (prop.type === NodeTypes.DIRECTIVE && prop.name === "model") models.push(prop.exp?.content);
      }
    }
    for (const child of node.children ?? []) visit(child);
  };
  visit(descriptor.template.ast);
  assert.ok(models.includes("form.cross_platform_memory_enabled"));
  assert.ok(relayManager);
  for (const file of ["./views/AssistantView.vue", "./api.ts", "./demo.ts"]) {
    const content = readFileSync(new URL(file, import.meta.url), "utf8");
    assert.ok(!content.includes("isolate_platform_contexts"), file);
    assert.ok(!content.includes("contextIsolationEnabled"), file);
    assert.ok(!content.includes("context-isolation"), file);
  }
});
