// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import * as vue from "vue";

const flush = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); };
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
};

function harness() {
  const requests = [], writes = [], notices = [], hooks = {};
  const botScope = vue.ref("");
  const scope = vue.effectScope();
  const context = vm.createContext({ exports: {}, console, require: name => {
    if (name === "vue") return { ...vue,
      onBeforeUnmount: fn => { hooks.unmount = fn; }, onActivated: fn => { hooks.activate = fn; }, onDeactivated: fn => { hooks.deactivate = fn; }
    };
    if (name === "../lib/bot-scope") return { botScope };
    if (name === "../api") return {
      getOwnSpace: profile => { const d = deferred(); requests.push({ profile, ...d }); return d.promise; },
      recordOwnSpaceWallet: (...args) => { const d = deferred(); writes.push({ args, ...d }); return d.promise; }
    };
    if (name === "../lib/toast") return { toastError: message => notices.push(message), toastSuccess: message => notices.push(message) };
    return {};
  }});
  const source = readFileSync(new URL("../views/SpaceView.vue", import.meta.url), "utf8").match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
  const script = source + "\nexport const harness = { space, loading, saving, grantOpen, grantAmount, grantReason, submit, reload };";
  scope.run(() => vm.runInContext(ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context));
  return { ...context.exports.harness, botScope, requests, writes, notices, hooks, stop: () => { hooks.unmount(); scope.stop(); } };
}
const wallet = cents => ({ enabled: true, wallet: { balance_cents: cents, recent: [] } });

test("all-profiles view never reads the backend's implicit default wallet", () => {
  const h = harness();
  try { assert.equal(h.requests.length, 0); assert.equal(h.loading.value, false); }
  finally { h.stop(); }
});

test("switching robots clears drafts and ignores out-of-order balances", async () => {
  const h = harness();
  try {
    h.botScope.value = "one"; await flush();
    h.grantAmount.value = 10; h.grantReason.value = "one only"; h.grantOpen.value = true;
    h.botScope.value = "two"; await flush();
    assert.equal(h.space.value, null); assert.equal(h.grantAmount.value, ""); assert.equal(h.grantReason.value, ""); assert.equal(h.grantOpen.value, false);
    h.requests[1].resolve(wallet(200)); await flush();
    h.requests[0].resolve(wallet(100)); await flush();
    assert.equal(h.space.value.wallet.balance_cents, 200); assert.equal(h.loading.value, false);
  } finally { h.stop(); }
});

test("late errors and post-unmount responses do not notify or replace data", async () => {
  const h = harness();
  h.botScope.value = "one"; await flush();
  h.botScope.value = "two"; await flush();
  h.requests[0].reject(new Error("old scope")); await flush();
  assert.equal(h.notices.length, 0);
  h.stop(); h.requests[1].resolve(wallet(500)); await flush();
  assert.equal(h.space.value, null);
});

test("wallet write stays bound to its original robot; switching cannot display its result", async () => {
  const h = harness();
  try {
    h.botScope.value = "one"; await flush(); h.requests[0].resolve(wallet(0)); await flush();
    h.grantAmount.value = 10;
    const writing = h.submit();
    await h.submit();
    assert.equal(h.writes.length, 1); assert.equal(h.writes[0].args[0], "one"); assert.equal(h.writes[0].args[2], 1000);
    h.botScope.value = "two"; await flush(); h.requests[1].resolve(wallet(200)); await flush();
    h.writes[0].resolve(wallet(1000)); await writing;
    assert.equal(h.space.value.wallet.balance_cents, 200); assert.equal(h.notices.length, 0); assert.equal(h.saving.value, false);
  } finally { h.stop(); }
});

test("returning from a cached view refreshes enabled state and balance", async () => {
  const h = harness();
  try {
    h.botScope.value = "one"; await flush(); h.requests[0].resolve(wallet(100)); await flush();
    h.hooks.activate(); assert.equal(h.requests.length, 1);
    h.hooks.deactivate(); h.hooks.activate(); assert.equal(h.requests.length, 2);
    h.requests[1].resolve({ ...wallet(200), enabled: false }); await flush();
    assert.equal(h.space.value.enabled, false);
  } finally { h.stop(); }
});
