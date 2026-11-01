import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { NodeTypes } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";

test("slang learning defaults on alongside memory settings without overriding explicit off", () => {
  const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
  const fallback = source.match(/slang_learning_enabled:\s*(config\.slang_learning_enabled\s*\?\?\s*true)/);
  assert.ok(fallback, "slang learning must default on with a nullish fallback");
  const resolve = new Function("config", `return ${fallback[1]}`);
  assert.equal(resolve({}), true);
  assert.equal(resolve({ slang_learning_enabled: false }), false);
  assert.equal(resolve({ slang_learning_enabled: true }), true);
  const memorySection = source.match(/<section[^>]*>(?:(?!<\/section>)[\s\S])*<h2>记忆<\/h2>[\s\S]*?<\/section>/)?.[0];
  assert.ok(memorySection?.includes('v-model="form.slang_learning_enabled"'));
  assert.ok(memorySection?.includes("群友明确解释或确认的叫法直接记入笔记本"));
  assert.ok(memorySection?.includes("不覆盖已有或已作废条目"));
  assert.ok(memorySection?.includes("共享范围遵循「笔记本跟随机器人」，默认群聊私聊共享"));
});

test("bot settings checkboxes have a visible switch track", () => {
  const source = readFileSync(new URL("../views/AssistantView.vue", import.meta.url), "utf8");
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
      const hasVisibleLabel = elements.some(child => hasClass(child, "switch-label"));
      const hasAriaLabel = input.props.some(prop =>
        (prop.type === NodeTypes.ATTRIBUTE && prop.name === "aria-label" && Boolean(prop.value?.content)) ||
        (prop.type === NodeTypes.DIRECTIVE && prop.name === "bind" && prop.arg?.content === "aria-label")
      );
      assert.ok(hasVisibleLabel || hasAriaLabel, `${name} is missing its label`);
      checked++;
    }
    for (const child of node.children ?? []) visit(child);
  };
  visit(descriptor.template.ast);
  assert.ok(checked > 0, "no bot settings switches were checked");
});
