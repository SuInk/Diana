import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { NodeTypes } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";

test("bot settings checkboxes have a visible switch track", () => {
  const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  const { descriptor, errors } = parse(source);
  assert.deepEqual(errors, []);

  const hasClass = (node, name) => node.props?.some(prop =>
    prop.type === NodeTypes.ATTRIBUTE && prop.name === "class" && prop.value?.content.split(/\s+/).includes(name)
  );
  let checked = 0;
  const visit = node => {
    if (node.type === NodeTypes.ELEMENT && node.tag === "label" && hasClass(node, "switch")) {
      const elements = node.children.filter(child => child.type === NodeTypes.ELEMENT);
      const inputIndex = elements.findIndex(child => child.tag === "input" && child.props.some(prop =>
        prop.type === NodeTypes.ATTRIBUTE && prop.name === "type" && prop.value?.content === "checkbox"
      ));
      assert.notEqual(inputIndex, -1, `switch at line ${node.loc.start.line} is missing its checkbox`);
      const input = elements[inputIndex];
      const binding = input.props.find(prop => prop.type === NodeTypes.DIRECTIVE && prop.name === "model");
      const name = binding?.exp?.content ?? `switch at line ${node.loc.start.line}`;
      // Shared CSS hides the input and styles only its adjacent track.
      const track = elements[inputIndex + 1];
      assert.ok(track && hasClass(track, "track"), `${name} is missing its adjacent visible track`);
      assert.ok(track.props.some(prop =>
        prop.type === NodeTypes.ATTRIBUTE && prop.name === "aria-hidden" && prop.value?.content === "true"
      ), `${name} track should be decorative`);
      assert.ok(elements.some(child => hasClass(child, "switch-label")), `${name} is missing its label`);
      checked++;
    }
    for (const child of node.children ?? []) visit(child);
  };
  visit(descriptor.template.ast);
  assert.ok(checked > 0, "no bot settings switches were checked");
});
