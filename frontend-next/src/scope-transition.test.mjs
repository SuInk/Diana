import assert from "node:assert/strict";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { beginScopeTransition, scopeSwitching, trackScopeRequest } from "./scope-transition.ts";

test("scope transition follows requests and ignores stale completions", async () => {
  const idle = trackScopeRequest();
  assert.equal(scopeSwitching.value, false);
  idle();

  beginScopeTransition();
  assert.equal(scopeSwitching.value, true);
  const first = trackScopeRequest();
  const second = trackScopeRequest();
  first();
  await delay(10);
  assert.equal(scopeSwitching.value, true);
  second();
  await delay(10);
  assert.equal(scopeSwitching.value, false);

  beginScopeTransition();
  const stale = trackScopeRequest();
  beginScopeTransition();
  const current = trackScopeRequest();
  stale();
  await delay(10);
  assert.equal(scopeSwitching.value, true);
  current();
  current();
  await delay(10);
  assert.equal(scopeSwitching.value, false);
});

test("cached, failed and chained reads all release the loading state", async () => {
  beginScopeTransition();
  await delay(10);
  assert.equal(scopeSwitching.value, false);

  beginScopeTransition();
  const failed = trackScopeRequest();
  try {
    throw new Error("request failed");
  } catch {
    // The API wrapper releases failed requests in finally.
  } finally {
    failed();
  }
  const chained = trackScopeRequest();
  await delay(10);
  assert.equal(scopeSwitching.value, true);
  chained();
  await delay(10);
  assert.equal(scopeSwitching.value, false);
});
