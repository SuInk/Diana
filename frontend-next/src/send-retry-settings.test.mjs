import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { sendRetryFields, sendRetryPayload, sendRetryValidationError, withUnsetSendRetryCleared } from "./send-retry-settings.ts";

test("empty send retry fields are saved as zero so the backend fills defaults or inherits", () => {
  const payload = sendRetryPayload({ send_backoff_initial_seconds: "", inbound_retry_max_attempts: 2 });
  assert.equal(payload.send_backoff_initial_seconds, 0);
  assert.equal(payload.send_backoff_max_seconds, 0);
  assert.equal(payload.inbound_retry_max_attempts, 2);
});

test("send retry validation rejects out-of-range and inverted intervals", () => {
  assert.equal(sendRetryValidationError({}), "");
  assert.equal(sendRetryValidationError({ send_failure_window_minutes: 0, send_drop_cooldown_minutes: "" }), "");
  assert.match(sendRetryValidationError({ inbound_retry_max_attempts: -1 }), /1 到 20/);
  assert.match(sendRetryValidationError({ inbound_retry_max_attempts: 1.5 }), /1 到 20/);
  assert.match(sendRetryValidationError({ inbound_retry_max_attempts: 21 }), /1 到 20/);
  assert.match(sendRetryValidationError({ send_backoff_initial_seconds: 2 }), /5 到 3600/);
  assert.match(sendRetryValidationError({ send_backoff_initial_seconds: 600, send_backoff_max_seconds: 60 }), /不能比首次间隔短/);
});

test("send retry defaults match the previous hard-coded constants", () => {
  const defaults = Object.fromEntries(sendRetryFields.map((field) => [field.key, field.fallback]));
  assert.deepEqual(defaults, {
    send_backoff_initial_seconds: 60,
    send_backoff_max_seconds: 900,
    send_failure_window_minutes: 30,
    send_drop_cooldown_minutes: 30,
    inbound_retry_max_attempts: 5
  });
});

test("zero send retry values are cleared before editing so the inherit placeholder shows", () => {
  const config = withUnsetSendRetryCleared({ send_failure_window_minutes: 0, inbound_retry_max_attempts: 3 });
  assert.deepEqual(config, { inbound_retry_max_attempts: 3 });
});

for (const view of ["AssistantView", "GroupsView"]) {
  test(`${view} renders and saves send retry settings`, async () => {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), "utf8");
    assert.match(source, /v-for="field in sendRetryFields"/);
    assert.match(source, /\.\.\.sendRetryPayload\(current\)/);
    assert.match(source, /sendRetryValidationError\(current\)/);
  });
}
