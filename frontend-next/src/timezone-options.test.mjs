import assert from "node:assert/strict";
import test from "node:test";
import { timezoneOptions } from "./timezone-options.ts";

test("UTC options include fractional offsets, defaults and preserve existing regions", () => {
 const options = timezoneOptions("America/New_York", "跟随系统默认");
 assert.equal(options[0].value, "");
 for (const value of ["UTC-12", "UTC", "UTC+8", "UTC+5:30", "UTC+5:45", "UTC+14", "America/New_York"]) {
  assert.ok(options.some(option => option.value === value), value);
 }
 assert.equal(new Set(options.map(option => option.value)).size, options.length);
 assert.equal(timezoneOptions("UTC+8", "默认").filter(option => option.value === "UTC+8").length, 1);
});
