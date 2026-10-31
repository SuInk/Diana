import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { createRenderer, defineAsyncComponent, h, nextTick } from "vue";
const compile = source => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;

test("stale resources reload once and share the backend cooldown; blocked storage prevents loops", () => {
  const storage = new Map(), listeners = {};
  let reloads = 0;
  const window = { sessionStorage: { getItem: k => storage.get(k), setItem: (k,v) => storage.set(k,v) },
    addEventListener: (name, fn) => { listeners[name] = fn; }, location: { reload: () => reloads++ } };
  const backend = vm.createContext({ exports: {}, window });
  vm.runInContext(compile(readFileSync(new URL("./backendState.ts", import.meta.url), "utf8")), backend);
  const recovery = vm.createContext({ exports: {}, window, require: () => backend.exports });
  vm.runInContext(compile(readFileSync(new URL("./page-load.ts", import.meta.url), "utf8")), recovery);
  recovery.exports.setupPageLoadRecovery();
  listeners["vite:preloadError"](); listeners["vite:preloadError"]();
  assert.equal(reloads, 1);
  assert.equal(backend.exports.autoReloadAllowed(), false);
  window.sessionStorage.getItem = () => { throw new Error("storage blocked"); };
  listeners["vite:preloadError"]();
  assert.equal(reloads, 1);
});
const node = text => ({ text, children: [], parent: null });
function remove(n) { if (n.parent) { n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null; } }
const renderer = createRenderer({
  createElement: node, createText: node, createComment: node,
  setText: (n, text) => { n.text = text; }, setElementText: (n, text) => { n.text = text; },
  parentNode: n => n.parent, nextSibling: () => null, patchProp() {}, remove,
  insert(n, parent) { remove(n); n.parent = parent; parent.children.push(n); }
});
const flush = async () => { await new Promise(resolve => setImmediate(resolve)); await nextTick(); };
test("page timeout and rejection replace skeleton; late success recovers", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const source = readFileSync(new URL("../App.vue", import.meta.url), "utf8");
  const lazy = source.match(/function lazyView\([\s\S]*?\n}/)[0];
  const context = vm.createContext({ exports: {}, defineAsyncComponent,
    ViewSkeleton: { render: () => h("div", "skeleton") }, ViewLoadError: { render: () => h("div", "error") } });
  vm.runInContext(compile(`${lazy}\nexport { lazyView };`), context);
  let resolve;
  const root = node("root");
  const app = renderer.createApp(context.exports.lazyView(() => new Promise(r => { resolve = r; })));
  app.config.errorHandler = () => {};
  app.mount(root); await flush();
  t.mock.timers.tick(120); await nextTick();
  assert.equal(root.children[0].text, "skeleton");
  t.mock.timers.tick(30_000); await nextTick();
  assert.equal(root.children[0].text, "error");
  resolve({ __esModule: true, default: { render: () => h("div", "records") } }); await flush(); await flush();
  assert.equal(root.children[0].text, "records");
  app.unmount();
  const failed = renderer.createApp(context.exports.lazyView(() => Promise.reject(new Error("missing chunk"))));
  failed.config.errorHandler = () => {};
  failed.mount(root); await flush();
  assert.equal(root.children[0].text, "error");
  failed.unmount();
});
