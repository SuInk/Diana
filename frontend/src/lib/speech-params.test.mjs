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
