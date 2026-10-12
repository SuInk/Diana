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

const at = (date) => new Date(date + "T12:00:00Z");
const option = (value, date) => timezoneOptions(undefined, "默认", at(date)).find(item => item.value === value);

test("regional options retain IANA identities as north and south seasons change", () => {
  for (const [value, winter, summer] of [
    ["America/New_York", "UTC-5", "UTC-4"],
    ["Europe/London", "UTC+0", "UTC+1"],
    ["Australia/Sydney", "UTC+11", "UTC+10"],
    ["Asia/Shanghai", "UTC+8", "UTC+8"]
  ]) {
    for (const [date, expected] of [["2026-01-15", winter], ["2026-07-15", summer]]) {
      const selected = option(value, date);
      assert.ok(selected, value);
      assert.equal(selected.value, value);
      assert.ok(selected.label.includes(`当前 ${expected} ·`), selected.label);
      assert.match(selected.hint, /地区规则/);
    }
  }
});

test("regional offsets update across both DST transitions, fixed offsets never change", () => {
  for (const [date, expected] of [["2026-03-07", "UTC-5"], ["2026-03-08", "UTC-4"], ["2026-10-31", "UTC-4"], ["2026-11-01", "UTC-5"]]) {
    assert.ok(option("America/New_York", date).label.includes(expected), date);
    const fixed = option("UTC-5", date);
    assert.equal(fixed.value, "UTC-5");
    assert.equal(fixed.label, "UTC-5");
    assert.match(fixed.hint, /全年不随夏令时变化/);
  }
});

test("aliases and legacy custom values stay selectable without conversion", () => {
  for (const value of ["US/Eastern", "UTC+08:00", "Legacy/Region"]) {
    const matching = timezoneOptions(value, "默认", at("2026-07-15")).filter(item => item.value === value);
    assert.equal(matching.length, 1);
  }
});
