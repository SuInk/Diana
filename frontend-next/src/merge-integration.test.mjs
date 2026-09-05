import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { NodeTypes } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";

test("group deletion renders only one confirmation dialog after merging view changes", () => {
  const { descriptor, errors } = parse(readFileSync(new URL("./views/GroupsView.vue", import.meta.url), "utf8"));
  assert.deepEqual(errors, []);
  let confirmations = 0;
  const visit = node => {
    if (node.type === NodeTypes.ELEMENT && node.tag === "Modal" && node.props.some(prop =>
      prop.type === NodeTypes.DIRECTIVE && prop.name === "if" && prop.exp?.content === "pendingDelete"
    )) confirmations++;
    for (const child of node.children ?? []) visit(child);
  };
  visit(descriptor.template.ast);
  assert.equal(confirmations, 1);
});
