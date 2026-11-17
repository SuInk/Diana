import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { reactive, readonly } from "vue";

// 在 vm 里跑 stream.ts：EventSource 和 document 换成能手动触发的替身，这样可以
// 直接投喂一帧 config_changed，看它有没有转成页面的配置刷新信号。
function streamHarness() {
  const changes = [];
  const listeners = {};
  class FakeEventSource {
    static CLOSED = 2;
    constructor(url) {
      this.url = url;
      this.readyState = 1;
    }
    addEventListener(name, handler) {
      (listeners[name] ??= []).push(handler);
    }
    close() {
      this.readyState = FakeEventSource.CLOSED;
    }
  }
  const source = readFileSync(new URL("./stream.ts", import.meta.url), "utf8")
    // CommonJS 里没有 import.meta；演示模式不是这条用例要测的东西。
    .replace("import.meta.env.VITE_DEMO_MODE", "undefined");
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
  const context = vm.createContext({
    exports: {},
    console,
    EventSource: FakeEventSource,
    document: { addEventListener() {}, visibilityState: "visible" },
    require: name => {
      if (name === "vue") return { reactive, readonly };
      if (name === "../api") return { scopeStatsSnapshot: () => null };
      if (name === "./configuration-sync") return { notifyConfigurationChanged: kind => changes.push(kind) };
      throw new Error(`unexpected import ${name}`);
    }
  });
  vm.runInContext(compiled, context);
  context.exports.startEventStream();
  return { changes, exports: context.exports, emit: (event, data) => listeners[event].forEach(handler => handler({ data })) };
}

test("a configuration change from elsewhere refreshes open pages; junk frames are ignored", () => {
  const { changes, emit } = streamHarness();
  // 主人在聊天里让机器人换了模型：后端播一条，页面按同一条路径重新拉配置。
  emit("config_changed", JSON.stringify({ kind: "bot" }));
  emit("config_changed", JSON.stringify({ kind: "llm" }));
  emit("config_changed", JSON.stringify({ kind: "nonsense" }));
  emit("config_changed", "not json");
  assert.deepEqual(changes, ["bot", "llm"]);
});

test("bot results and trace progress both reach live-event subscribers, one call per frame", () => {
  const { emit, exports } = streamHarness();
  const seen = [];
  const stop = exports.onLiveEvent((ref, source) => seen.push(`${ref.message_id}:${source}`));
  emit("bot_event", JSON.stringify({ at: "2026-11-17T10:00:00Z", kind: "group", message_id: "30001", handled: true }));
  // 同一时刻来的两条都要送到，不能只剩最后一条。
  emit("bot_event", JSON.stringify({ at: "2026-11-17T10:00:00Z", kind: "group", message_id: "30002", handled: true }));
  emit("event_progress", JSON.stringify({ kind: "group", message_id: "30003" }));
  emit("event_progress", "not json");
  stop();
  emit("event_progress", JSON.stringify({ kind: "group", message_id: "30004" }));
  assert.deepEqual(seen, ["30001:result", "30002:result", "30003:progress"]);
});
