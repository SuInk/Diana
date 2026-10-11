import assert from "node:assert/strict";
import test from "node:test";
import { mergeResolverDemoSettings, resolverDemoManifest, resolverDemoTestResponse } from "./resolver-demo.ts";
import { resolverCheckLabel } from "./resolver-credentials.ts";

const keys = ["bili_sessdata", "douyin_cookie", "xhs_cookie", "ytdlp_cookies_path"];
async function checks(saved = {}, payload = {}, selected = []) {
  const response = await resolverDemoTestResponse(saved, payload, selected);
  assert.equal(response.status, 200);
  return (await response.json()).credentials;
}

test("resolver demo exposes the complete real settings schema for its settings deep link", () => {
  assert.equal(resolverDemoManifest.id, "official.nonebot-plugin-resolver-go");
  const settings = new Map(resolverDemoManifest.settings.map(spec => [spec.key, spec]));
  assert.equal(settings.size, 24);
  assert.equal(settings.size, resolverDemoManifest.settings.length);
  assert.deepEqual([...settings.keys()], [
    "ask_agent", "follow_up_comment", "download_media", "merged_forward", "max_video_mb",
    "max_video_duration_seconds", "max_images", "platform_level_rules", "max_video_height",
    "fetch_title", "max_links", "timeout_seconds", "browser_render", "browser_cdp_url",
    "enabled_platforms", "summary_max_runes", "cache_ttl_minutes", "user_agent", ...keys,
    "proxy_url", "ytdlp_cookies_from_browser"
  ]);
  assert.deepEqual(settings.get("enabled_platforms").default, ["bilibili", "youtube", "x", "xiaohongshu", "zhihu", "weibo", "douyin", "github"]);
  assert.deepEqual(settings.get("enabled_platforms").options, settings.get("platform_level_rules").options);
  assert.deepEqual(resolverDemoManifest.settings.filter(spec => spec.secret).map(spec => spec.key), keys.slice(0, 3));
  assert.equal(settings.get("max_video_mb").default, 200);
  assert.equal(settings.get("max_video_duration_seconds").default, 900);
  assert.equal(settings.get("timeout_seconds").max, 120);
});

test("missing credentials stay unconfigured and omitted filter returns all four", async () => {
  const result = await checks();
  assert.deepEqual(result.map(check => check.key), keys);
  for (const check of result) {
    assert.equal(check.configured, false);
    assert.equal(check.state, "unconfigured");
    assert.equal(check.account, undefined);
  }
});

test("filter only checks the requested key even with other drafts and disabled platforms", async () => {
  const result = await checks({}, { settings: { bili_sessdata: "demo-valid:fast", douyin_cookie: "demo-http-error:slow", enabled_platforms: [] } }, ["bili_sessdata"]);
  assert.equal(result.length, 1);
  assert.equal(result[0].key, "bili_sessdata");
  assert.equal(result[0].state, "valid");
});

test("unknown, explicit empty, whitespace and duplicate credential filters return 400", async () => {
  for (const selected of [[""], [" "], ["unknown"], ["BILI_SESSDATA"], ["bili_sessdata", "bili_sessdata"], ["bili_sessdata", "xhs_cookie"]]) {
    const response = await resolverDemoTestResponse({}, {}, selected);
    assert.equal(response.status, 400, JSON.stringify(selected));
  }
});

test("test merges saved plus drafts, blank secrets retain saved, and clears win without persisting", async () => {
  const saved = { bili_sessdata: "demo-valid:fast", douyin_cookie: "demo-invalid:fast", ytdlp_cookies_path: "demo-valid:fast", max_links: 7 };
  const before = structuredClone(saved);
  const result = await checks(saved, { settings: { bili_sessdata: " \n", douyin_cookie: "demo-valid:fast", xhs_cookie: "demo-invalid:fast", ytdlp_cookies_path: "" }, clear_secrets: [" xhs_cookie "] });
  assert.deepEqual(result.map(check => check.state), ["valid", "valid", "unconfigured", "unconfigured"]);
  assert.deepEqual(saved, before);
  assert.deepEqual((await checks(saved, {}, ["douyin_cookie"]))[0].state, "invalid");
  assert.deepEqual(mergeResolverDemoSettings(saved, { settings: {} }, "test"), saved);
  assert.equal(mergeResolverDemoSettings(saved, { clear_secrets: ["bili_sessdata"] }, "test").bili_sessdata, undefined);
});

test("save keeps blank or omitted secrets globally, replaces normal settings and applies explicit clears", () => {
  const saved = { bili_sessdata: "demo-valid", xhs_cookie: "demo-invalid", max_links: 7, ytdlp_cookies_path: "demo-valid" };
  assert.deepEqual(mergeResolverDemoSettings(saved, { settings: { bili_sessdata: " \n", max_links: 4 } }, "save"), { bili_sessdata: "demo-valid", xhs_cookie: "demo-invalid", max_links: 4 });
  assert.deepEqual(mergeResolverDemoSettings(saved, { settings: {}, clear_secrets: [" bili_sessdata "] }, "save"), { xhs_cookie: "demo-invalid" });
  assert.deepEqual(mergeResolverDemoSettings(saved, { settings: { xhs_cookie: "demo-valid" }, clear_secrets: ["xhs_cookie"] }, "save"), { bili_sessdata: "demo-valid" });
  assert.equal(saved.xhs_cookie, "demo-invalid");
});

test("synthetic scenarios distinguish valid, invalid, unverified, probe errors and request errors", async () => {
  for (const state of ["valid", "invalid", "unverified", "error"]) {
    const [check] = await checks({}, { settings: { bili_sessdata: `demo-${state}:fast` } }, ["bili_sessdata"]);
    assert.equal(check.state, state);
    assert.equal(check.configured, true);
    assert.equal(Boolean(check.account), state === "valid");
  }
  const response = await resolverDemoTestResponse({}, { settings: { bili_sessdata: "demo-http-error:fast" } }, ["bili_sessdata"]);
  assert.equal(response.status, 503);
  assert.match((await response.json()).error, /演示/);
  const [unknown] = await checks({}, { settings: { bili_sessdata: "synthetic-unrecognized-value" } }, ["bili_sessdata"]);
  assert.equal(unknown.state, "unverified");
  assert.match(unknown.message, /不会检测真实凭据/);
});

test("yt-dlp simulated readable Netscape file never claims a verified login", async () => {
  const [check] = await checks({}, { settings: { ytdlp_cookies_path: "demo-valid:fast" } }, ["ytdlp_cookies_path"]);
  assert.equal(check.state, "unverified");
  assert.equal(check.account, undefined);
  assert.match(check.message, /Netscape/);
});

test("an unread demo file is not reported as format-checked, while unverified Netscape results remain compatible", async () => {
  const [unread] = await checks({}, { settings: { ytdlp_cookies_path: "/synthetic/cookies.txt" } }, ["ytdlp_cookies_path"]);
  assert.equal(unread.state, "unverified");
  assert.equal(unread.file_format_checked, false);
  assert.match(unread.message, /未执行文件格式检查/);
  assert.equal(resolverCheckLabel({ phase: "done", check: { ...unread, message: "arbitrary message" } }, true), "未执行文件检查");
  for (const scenario of ["valid", "unverified"]) {
    const [checked] = await checks({}, { settings: { ytdlp_cookies_path: `demo-${scenario}:fast` } }, ["ytdlp_cookies_path"]);
    assert.equal(checked.state, "unverified");
    assert.equal(checked.file_format_checked, undefined);
    assert.equal(resolverCheckLabel({ phase: "done", check: checked }, true), "文件格式正常");
  }
});

test("synthetic slow responses can complete out of order and abort without altering saved settings", async () => {
  const finished = [];
  const saved = { bili_sessdata: "demo-invalid:fast" };
  const slow = checks(saved, { settings: { bili_sessdata: "demo-valid:slow" } }, ["bili_sessdata"]).then(result => { finished.push("slow"); return result; });
  const fast = checks(saved, {}, ["bili_sessdata"]).then(result => { finished.push("fast"); return result; });
  const [slowResult, fastResult] = await Promise.all([slow, fast]);
  assert.deepEqual(finished, ["fast", "slow"]);
  assert.equal(slowResult[0].state, "valid");
  assert.equal(fastResult[0].state, "invalid");
  assert.equal(saved.bili_sessdata, "demo-invalid:fast");

  const controller = new AbortController();
  const pending = resolverDemoTestResponse(saved, { settings: { bili_sessdata: "demo-valid:slow" } }, ["bili_sessdata"], controller.signal);
  controller.abort();
  await assert.rejects(pending, { name: "AbortError" });
  await assert.rejects(resolverDemoTestResponse({}, {}, [], controller.signal), { name: "AbortError" });
});
