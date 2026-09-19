import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("./views/AssistantView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);

function loadFunction(name, context) {
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
  assert.ok(fn, `missing ${name}`);
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return context[name];
}

function loadWarningFor(context) {
  for (const name of ["endpointHostname", "isLocalOriginHost", "oneBotMediaOriginWarningText"]) {
    loadFunction(name, context);
  }
  return context.oneBotMediaOriginWarningText;
}

// 警告条逻辑是纯函数：正向 ws / HTTP 填外机地址时显式提醒，回环、docker 内网
// 名、以及和浏览器当前访问 Diana 用的主机名一致时不打扰。
test("cross-machine forward ws and http endpoints warn about media origin", () => {
  const context = vm.createContext({ URL });
  const warningFor = loadWarningFor(context);
  const warn = "媒体回源基址";

  // 反向 ws 永远不警告（媒体回源按握手地址推断，不需要用户干预）。
  assert.equal(warningFor("reverse_ws", "ws://napcat.example.com:6700", "", "diana.local"), "");
  // 正向 ws 填另一台主机：警告，且文案里带显式配置项。
  const forward = warningFor("forward_ws", "ws://napcat.example.com:6700", "", "diana.local");
  assert.ok(forward.includes("napcat.example.com"));
  assert.ok(forward.includes(warn));
  // HTTP 接入同理。
  const http = warningFor("http", "", "http://192.168.1.20:5700", "diana.local");
  assert.ok(http.includes("192.168.1.20"));
  assert.ok(http.includes(warn));
});

test("local-looking endpoints and Diana's own hostname stay silent", () => {
  const context = vm.createContext({ URL });
  const warningFor = loadWarningFor(context);

  for (const endpoint of ["ws://127.0.0.1:6700", "ws://localhost:6700", "ws://host.docker.internal:3001"]) {
    assert.equal(warningFor("forward_ws", endpoint, "", "192.168.1.10"), "", endpoint);
  }
  // 浏览器访问 Diana 用的就是这台主机：接入端回源同一主机没问题。
  assert.equal(warningFor("forward_ws", "ws://diana.local:6700", "", "diana.local"), "");
  // 空地址还没填：不警告（保存时另有 URL 校验）。
  assert.equal(warningFor("forward_ws", "", "", "diana.local"), "");
  // 填了但解析不出主机名：也不警告。
  assert.equal(warningFor("forward_ws", "not a url", "", "diana.local"), "");
});
