// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import * as vue from "vue";
import { roomActivity } from "./room-activity.ts";

function harness() {
  const props = vue.reactive({ profile: "one", enabled: true });
  const stream = vue.reactive({ connected: false, status: null, events: [] });
  const hooks = {}, listeners = {}, timers = new Set();
  let nextTimer = 0;
  const media = { matches: false, addEventListener: (_, fn) => { listeners.motion = fn; }, removeEventListener: () => { delete listeners.motion; } };
  const document = { hidden: false, addEventListener: (_, fn) => { listeners.visibility = fn; }, removeEventListener: () => { delete listeners.visibility; } };
  const scope = vue.effectScope();
  const context = vm.createContext({ exports: {}, console, document, defineProps: () => props, window: { matchMedia: () => media },
    setInterval: () => { const id = ++nextTimer; timers.add(id); return id; }, clearInterval: id => timers.delete(id),
    require: name => {
      if (name === "vue") return { ...vue, onMounted: fn => { hooks.mount = fn; }, onActivated: fn => { hooks.activate = fn; }, onDeactivated: fn => { hooks.deactivate = fn; }, onBeforeUnmount: fn => { hooks.unmount = fn; } };
      if (name === "../lib/stream") return { stream };
      if (name === "../lib/room-activity") return { roomActivity };
      return {};
    }
  });
  const source = readFileSync(new URL("../components/OwnSpaceRoom.vue", import.meta.url), "utf8").match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1].replaceAll("import.meta.env.VITE_DEMO_MODE", '"false"');
  const script = source + "\nexport const harness = { followAgent, takeControl, mode, previewPose, paused, pose, motionOff };";
  scope.run(() => vm.runInContext(ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context));
  hooks.mount(); hooks.activate();
  return { ...context.exports.harness, hooks, props, stream, timers, listeners, media, document, stop: () => { hooks.unmount(); scope.stop(); } };
}

test("room clock stops while hidden, kept-alive or unmounted", async () => {
  const h = harness();
  try {
    assert.equal(h.timers.size, 1);
    h.document.hidden = true; h.listeners.visibility(); assert.equal(h.timers.size, 0);
    h.document.hidden = false; h.listeners.visibility(); assert.equal(h.timers.size, 1);
    h.hooks.deactivate(); assert.equal(h.timers.size, 0);
    h.listeners.visibility(); assert.equal(h.timers.size, 0);
    h.hooks.activate(); assert.equal(h.timers.size, 1);
  } finally { h.stop(); }
  assert.equal(h.timers.size, 0); assert.deepEqual(Object.keys(h.listeners), []);
});

test("preview is local, respects reduced motion, and resets on scope or enable changes", async () => {
  const h = harness();
  try {
    h.mode.value = "preview"; h.previewPose.value = "desk";
    assert.equal(h.pose.value, "desk"); assert.equal(h.paused.value, false);
    h.media.matches = true; h.listeners.motion(); assert.equal(h.paused.value, true);
    h.media.matches = false; h.listeners.motion();
    h.motionOff.value = true; assert.equal(h.paused.value, true);
    h.props.profile = "two"; await vue.nextTick();
    assert.equal(h.mode.value, "auto"); assert.equal(h.previewPose.value, "rest");
    h.mode.value = "preview"; h.previewPose.value = "phone"; h.props.enabled = false; await vue.nextTick();
    assert.equal(h.mode.value, "auto"); assert.equal(h.pose.value, "rest"); assert.equal(h.paused.value, true);
  } finally { h.stop(); }
});

test("defaults to following and releases manual display on return", () => {
  const h = harness();
  try {
    assert.equal(h.mode.value, "auto"); assert.equal(h.motionOff.value, false);
    h.takeControl(); assert.equal(h.mode.value, "preview");
    h.previewPose.value = "desk"; h.motionOff.value = true;
    h.followAgent(); assert.equal(h.mode.value, "auto"); assert.equal(h.motionOff.value, false);
    h.takeControl(); h.motionOff.value = true;
    h.hooks.deactivate(); h.hooks.activate();
    assert.equal(h.mode.value, "auto"); assert.equal(h.motionOff.value, false);
  } finally { h.stop(); }
});

test("manual display holds its pose while snapshots advance; return follows latest", () => {
  const h = harness();
  try {
    h.stream.connected = true;
    h.stream.status = { running: true, channel: { profile_id: "one", connected: true }, rooms: [
      { profile_id: "one", epoch: "boot", revision: 1, enabled: true, pose: "phone", source: "agent", phase: "idle", active_tasks: 0 }
    ] };
    assert.equal(h.pose.value, "phone"); assert.equal(h.paused.value, false);
    h.takeControl(); assert.equal(h.pose.value, "phone");
    h.stream.status.rooms[0].pose = "desk";
    h.stream.status.rooms[0].revision++;
    assert.equal(h.pose.value, "phone");
    h.followAgent(); assert.equal(h.pose.value, "desk");
    h.media.matches = true; h.listeners.motion();
    h.takeControl(); h.followAgent(); assert.equal(h.paused.value, true);
  } finally { h.stop(); }
});
