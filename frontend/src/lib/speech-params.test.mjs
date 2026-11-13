import test from "node:test";
import assert from "node:assert/strict";
import { speechServiceFields, mediaParamFields, resolveSpeechAPI, updateSpeechParam } from "./speech-params.ts";

test("provider speech settings expose service fields without robot behavior", () => {
  const fields = speechServiceFields("tts", {}, "https://api.breeze.blue/v1");
  const keys = fields.map(field => field.key);
  for (const key of ["api", "voice", "format", "speed", "instructions", "language", "timeout_seconds"]) assert.ok(keys.includes(key), key);
  for (const key of ["send_mode", "max_chars"]) assert.ok(!keys.includes(key), key);
  assert.deepEqual(fields.find(field => field.key === "format").options.map(option => option.value), ["", "mp3", "wav"]);
  assert.ok(mediaParamFields("tts", "breeze").some(field => field.key === "send_mode"));
  const stt = speechServiceFields("stt", { api: "openai" }, "");
  assert.ok(stt.some(field => field.key === "prompt"));
  assert.ok(stt.every(field => !["private_enabled", "max_duration_seconds"].includes(field.key)));
});

test("provider protocol selection supports proxies and rejects lookalike hosts", () => {
  assert.equal(resolveSpeechAPI("", "https://api.breeze.blue/v1"), "breeze");
  assert.equal(resolveSpeechAPI("elevenlabs", "https://api.breeze.blue"), "elevenlabs");
  assert.equal(resolveSpeechAPI("", "https://breeze.blue.example.com"), "openai");
});

test("changing request templates preserves voice but removes previous request fields", () => {
  const original = {voice:"alice", method:"GET", body_type:"query", path:"old", body:"old", response:"url:old"};
  const next = updateSpeechParam(original, "template", "xtts");
  assert.equal(next.voice, "alice");
  assert.equal(next.path, "tts_to_audio/");
  assert.equal(next.method, undefined);
  assert.equal(next.body_type, undefined);
  assert.equal(original.path, "old");
  assert.equal(updateSpeechParam(next, "voice", "  ").voice, undefined);
});

test("deepseek speech is detected by host and hides unsupported fields", () => {
  assert.equal(resolveSpeechAPI("", "https://api.deepseek.com"), "deepseek");
  const tts = mediaParamFields("tts", "deepseek").map(field => field.key);
  assert.ok(tts.includes("voice"));
  for (const key of ["speed", "format", "instructions"]) assert.ok(!tts.includes(key), key);
  assert.ok(mediaParamFields("stt", "deepseek").some(field => field.key === "api" && field.options.some(option => option.value === "deepseek")));
});

test("video slot shows only the fields the selected vendor needs", () => {
  const keys = api => mediaParamFields("video", api).map(field => field.key);
  assert.deepEqual(keys(""), ["api", "size", "seconds", "timeout_seconds", "poll_interval_seconds"]);
  assert.ok(keys("kling").includes("ratio") && keys("kling").includes("resolution"));
  assert.ok(!keys("kling").includes("submit_body"));
  assert.ok(!keys("runway").includes("ratio"));
  for (const key of ["template", "auth", "submit_path", "submit_body", "id_path", "status_path", "status_field", "status_map", "video_url_path", "download_auth"]) assert.ok(keys("custom").includes(key), key);
  assert.match(mediaParamFields("video", "kling")[0].hint, /AccessKey:SecretKey/);
  // 密钥只在提供商配置里，插槽参数不出现任何 Key 字段。
  assert.ok(keys("custom").every(key => !/secret|api_key|access/.test(key)));
  assert.equal(updateSpeechParam({}, "template", "vidu").template, "vidu");
  // 提供商页的视频服务参数用同一套字段，不按地址猜接口。
  assert.deepEqual(speechServiceFields("video", { api: "kling" }, "https://api.minimax.io").map(field => field.key), keys("kling"));
  assert.deepEqual(speechServiceFields("video", {}, "https://api.minimax.io").map(field => field.key), keys(""));
});
