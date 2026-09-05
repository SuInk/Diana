import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { after, before, test } from "node:test";
import { createServer } from "vite";
import { createSSRApp } from "vue";
import { renderToString } from "@vue/server-renderer";
import { NodeTypes } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import { parse as parseCSS } from "postcss";

let server;
let LoadingSkeleton;
let StatCard;

before(async () => {
  server = await createServer({
    server: { middlewareMode: true },
    appType: "custom",
    optimizeDeps: { noDiscovery: true, include: [] }
  });
  LoadingSkeleton = (await server.ssrLoadModule("/src/components/LoadingSkeleton.vue")).default;
  StatCard = (await server.ssrLoadModule("/src/components/StatCard.vue")).default;
});

after(async () => { await server?.close(); });

const render = (component, props) => renderToString(createSSRApp(component, props));

test("collection skeletons reuse their content layouts without interactive placeholders", async () => {
  const layouts = {
    bots: "bot-profile-grid",
    groups: "group-grid",
    plugins: "plugin-tiles",
    providers: "row-list",
    users: "log-row",
    notebook: "log-row",
    logs: "log-time",
    tasks: "task-list",
    sessions: "session-list",
    form: "form-grid",
    chart: "spark-plot",
    feed: "event-feed"
  };
  for (const [kind, layout] of Object.entries(layouts)) {
    const html = await render(LoadingSkeleton, { kind, count: 2, label: "Loading content" });
    assert.ok(html.includes(layout), `${kind} must retain the content layout`);
    assert.ok(html.includes('role="status"') && html.includes('aria-label="Loading content"'));
    assert.ok(html.includes('aria-hidden="true"'));
    assert.ok(!/<(?:button|input|a)\b/.test(html), `${kind} must not create interactive placeholders`);
  }
});

test("plugin skeleton follows the saved arrangement", async () => {
  const html = await render(LoadingSkeleton, { kind: "plugins", layout: "rows", count: 4 });
  assert.ok(html.includes('class="plugin-rows"'));
  assert.equal((html.match(/class="plugin-card"/g) ?? []).length, 4);
  assert.ok(html.includes('class="plugin-card-bottom"'));
});

test("stat skeleton keeps the label and hides pending zero values", async () => {
  const props = { label: "Messages", value: "0", foot: "Total 0", loading: true };
  const pending = await render(StatCard, props);
  assert.ok(pending.includes("Messages"));
  assert.ok(pending.includes('aria-busy="true"'));
  assert.ok(pending.includes("skeleton-inline"));
  assert.ok(pending.includes('class="skeleton skeleton-text" aria-hidden="true">Total 0</span>'));
  const loaded = await render(StatCard, { ...props, loading: false });
  assert.ok(loaded.includes("Total 0"));
  assert.ok(!loaded.includes("skeleton-block"));
});

test("bot card actions are matching buttons with an explicitly destructive delete action", () => {
  const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
  const { descriptor, errors } = parse(source);
  assert.deepEqual(errors, []);
  const classes = node => node.props?.find(prop => prop.type === NodeTypes.ATTRIBUTE && prop.name === "class")?.value?.content.split(/\s+/) ?? [];
  let actions;
  const visit = node => {
    if (classes(node).includes("bot-profile-actions")) actions = node;
    for (const child of node.children ?? []) visit(child);
  };
  visit(descriptor.template.ast);
  assert.ok(actions);
  const buttons = actions.children.filter(child => child.type === NodeTypes.ELEMENT);
  assert.equal(buttons.length, 2);
  for (const button of buttons) {
    assert.equal(button.tag, "button");
    assert.ok(classes(button).includes("btn") && classes(button).includes("small"));
    assert.ok(!classes(button).includes("ghost") && !classes(button).includes("icon-only"));
    assert.ok(button.children.some(child => child.type === NodeTypes.TEXT && child.content.trim()));
  }
  assert.ok(classes(buttons[1]).includes("danger"));
});

test("danger colors survive ghost styles and skeleton motion respects user preferences", () => {
  const css = parseCSS(readFileSync(new URL("./styles/main.css", import.meta.url), "utf8"));
  for (const selector of [".btn.danger", ".btn.ghost.danger", ".btn.ghost.danger:hover:not(:disabled)"]) {
    const rules = [];
    css.walkRules(selector, rule => rules.push(rule));
    assert.ok(rules.some(rule => rule.nodes.some(decl => decl.prop === "color" && decl.value === "var(--err)")), selector);
  }
  let reducedMotion = false;
  css.walkAtRules("media", rule => {
    if (rule.params !== "(prefers-reduced-motion: reduce)") return;
    rule.walkRules(".skeleton", skeleton => {
      reducedMotion = skeleton.nodes.some(decl => decl.prop === "animation" && decl.value === "none");
    });
  });
  assert.ok(reducedMotion);
});
