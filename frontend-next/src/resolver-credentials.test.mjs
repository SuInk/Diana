import assert from "node:assert/strict";
import test from "node:test";
import { reactive } from "vue";
import { createResolverCredentialChecks, groupResolverSettings, resolverCredentialKeys, resolverCredentialSource, resolverCheckLabel } from "./resolver-credentials.ts";

const key = "bili_sessdata";
const tick = () => Promise.resolve();
function fixture() {
  const inputs = reactive(Object.fromEntries(resolverCredentialKeys.map((key) => [key, { value: "", configured: true, clearing: false }])));
  const calls = [];
  const checks = createResolverCredentialChecks({ input: (key) => inputs[key], request: (key, signal) => new Promise((resolve, reject) => calls.push({ key, signal, resolve, reject, value: inputs[key].value })) });
  checks.open();
  return { inputs, calls, checks };
}
const result = (key, state = "valid", message = "mock account response") => ({ key, label: key, state, configured: true, message });

test("opening and unchanged blur never scan; edited blur checks only one platform once", async () => {
  const { inputs, calls, checks } = fixture();
  await tick();
  await checks.test(key, "blur");
  assert.equal(calls.length, 0);
  inputs[key].value = "synthetic-draft";
  assert.equal(checks.states[key].phase, "pending");
  assert.equal(calls.length, 0);
  const first = checks.test(key, "blur");
  const manual = checks.test(key);
  assert.equal(first, manual);
  await tick();
  assert.deepEqual(calls.map((c) => c.key), [key]);
  assert.equal(checks.states.douyin_cookie.phase, "idle");
  calls[0].resolve(result(key));
  await first;
  await checks.test(key, "blur");
  assert.equal(calls.length, 1);
  assert.equal(checks.states[key].check.state, "valid");
  checks.dispose();
});

test("first manual check of saved blank credential succeeds without any input revision", async () => {
  const { calls, checks } = fixture();
  const request = checks.test(key);
  await tick();
  calls[0].resolve(result(key));
  await request;
  assert.equal(checks.states[key].phase, "done");
  assert.equal(checks.states[key].check.state, "valid");
  checks.dispose();
});

test("input mutation invalidates immediately; stale success/failure/finally cannot replace new loading or results", async () => {
  for (const reject of [false, true]) {
    const { inputs, calls, checks } = fixture();
    inputs[key].value = "synthetic-old";
    const old = checks.test(key);
    await tick();
    inputs[key].value = "synthetic-new";
    assert.equal(calls[0].signal.aborted, true);
    assert.deepEqual({ ...checks.states[key] }, { phase: "pending" });
    const current = checks.test(key, "blur");
    await tick();
    reject ? calls[0].reject(new Error("old failure")) : calls[0].resolve(result(key, "invalid"));
    await old;
    assert.equal(checks.states[key].phase, "checking");
    assert.equal(checks.test(key), current);
    calls[1].resolve(result(key));
    await current;
    assert.equal(checks.states[key].check.state, "valid");
    checks.dispose();
  }
});

test("blank means saved credential, explicit clearing prevents network and cancel clearing invalidates", async () => {
  const { inputs, calls, checks } = fixture();
  inputs[key].value = "synthetic-draft";
  const first = checks.test(key);
  await tick();
  inputs[key].clearing = true;
  inputs[key].value = "";
  assert.equal(calls[0].signal.aborted, true);
  await checks.test(key);
  calls[0].resolve(result(key));
  await first;
  assert.equal(calls.length, 1);
  assert.equal(checks.states[key].check, undefined);
  assert.match(resolverCredentialSource(inputs[key]), /待清除/);
  inputs[key].clearing = false;
  assert.equal(checks.states[key].phase, "pending");
  assert.match(resolverCredentialSource(inputs[key]), /留空沿用/);
  await checks.test(key, "blur");
  assert.equal(calls.length, 1);
  const saved = checks.test(key);
  await tick();
  calls[1].resolve(result(key));
  await saved;
  inputs[key].configured = false;
  await checks.test(key);
  assert.equal(calls.length, 2);
  checks.dispose();
});

test("close/reopen, scope switch, deactivation and disposal ignore transports that do not honor abort", async () => {
  for (const action of ["close", "reset", "dispose"]) {
    const { inputs, calls, checks } = fixture();
    inputs[key].value = "synthetic-old";
    const old = checks.test(key);
    await tick();
    checks[action]();
    assert.equal(calls[0].signal.aborted, true);
    if (action !== "dispose") checks.open();
    calls[0].resolve(result(key, "invalid"));
    await old;
    assert.equal(checks.states[key].check, undefined);
    assert.equal(checks.states[key].phase, "idle");
    checks.dispose();
  }
});

test("cancellation before request dispatch makes no network request", async () => {
  const { inputs, calls, checks } = fixture();
  inputs[key].value = "synthetic-draft";
  const request = checks.test(key, "blur");
  checks.close();
  await request;
  assert.equal(calls.length, 0);
  checks.dispose();
});

test("manual retry after request error is allowed; automatic blur still only tries the revision once", async () => {
  const { inputs, calls, checks } = fixture();
  inputs[key].value = "synthetic-draft";
  const first = checks.test(key, "blur");
  await tick();
  calls[0].reject(new Error("synthetic timeout"));
  await first;
  assert.equal(checks.states[key].phase, "failed");
  assert.equal(resolverCheckLabel(checks.states[key]), "请求失败");
  await checks.test(key, "blur");
  assert.equal(calls.length, 1);
  const retry = checks.test(key);
  await tick();
  calls[1].resolve(result(key, "unverified"));
  await retry;
  assert.equal(resolverCheckLabel(checks.states[key]), "暂时无法判断");
  checks.dispose();
});

test("reset defaults aborts all platforms; unconfigured and clear fields do not request", async () => {
  const { inputs, calls, checks } = fixture();
  const first = checks.test(key);
  const second = checks.test("douyin_cookie");
  await tick();
  checks.reset();
  inputs[key].clearing = true;
  inputs.douyin_cookie.clearing = true;
  await checks.test(key);
  calls.forEach((call) => { assert.equal(call.signal.aborted, true); call.resolve(result(call.key)); });
  await Promise.all([first, second]);
  assert.equal(checks.states[key].check, undefined);
  assert.equal(checks.states.douyin_cookie.check, undefined);
  assert.equal(calls.length, 2);
  checks.dispose();
});

test("file input is manually checked and never claims login; saved file is not an unsaved draft", async () => {
  const { inputs, calls, checks } = fixture();
  const file = "ytdlp_cookies_path";
  inputs[file].configured = false;
  inputs[file].savedValue = "/synthetic/cookies.txt";
  inputs[file].value = "/synthetic/cookies.txt";
  assert.equal(resolverCredentialSource(inputs[file]), "已配置");
  await checks.test(file, "blur");
  assert.equal(calls.length, 0);
  inputs[file].value = "/synthetic/other.txt";
  assert.equal(resolverCredentialSource(inputs[file]), "未保存");
  const request = checks.test(file);
  await tick();
  calls[0].resolve(result(file, "unverified"));
  await request;
  assert.equal(resolverCheckLabel(checks.states[file], true), "文件格式正常");
  assert.doesNotMatch(resolverCheckLabel(checks.states[file], true), /登录/);
  inputs[file].value = "";
  await checks.test(file);
  assert.equal(calls.length, 1);
  checks.dispose();
});

test("manifest groups render every setting exactly once, preserving future settings in advanced", () => {
  const specs = ["enabled_platforms", ...resolverCredentialKeys, "download_media", "platform_level_rules", "future_option", "browser_render"].map((key) => ({ key, label: key, type: "string", default: "" }));
  const groups = groupResolverSettings(specs);
  const keys = [...groups.platforms, ...groups.credentials.map((item) => item.spec), ...groups.media, ...groups.advanced].map((spec) => spec.key);
  assert.deepEqual(keys.toSorted(), specs.map((spec) => spec.key).toSorted());
  assert.equal(new Set(keys).size, keys.length);
  assert.ok(groups.advanced.some((spec) => spec.key === "future_option"));
});
