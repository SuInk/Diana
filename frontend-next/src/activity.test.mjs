import assert from "node:assert/strict";
import test from "node:test";
import { activityLevel, activityTimezoneNote, buildActivityCalendar } from "./activity.ts";

test("activity level splits by share of peak and keeps zero distinct", () => {
  assert.equal(activityLevel(0, 100), 0);
  assert.equal(activityLevel(1, 100), 1);
  assert.equal(activityLevel(26, 100), 2);
  assert.equal(activityLevel(100, 100), 4);
  assert.equal(activityLevel(5, 0), 0);
});

test("calendar ends on today's week, starts on Monday, draws every past day", () => {
  // 2026-10-07 是周三。
  const weeks = buildActivityCalendar("2026-10-07", [
    { date: "2026-09-30", count: 4 },
    { date: "2026-10-07", count: 9 }
  ], 3);
  assert.equal(weeks.length, 3);
  assert.equal(weeks[0].days[0].date, "2026-09-21");
  const flat = weeks.flatMap((week) => week.days);
  const byDate = Object.fromEntries(flat.map((cell) => [cell.date, cell.count]));
  assert.equal(byDate["2026-09-21"], 0, "没有消息的过去日子是灰色空格");
  assert.equal(byDate["2026-09-30"], 4);
  assert.equal(byDate["2026-10-07"], 9);
  assert.equal(byDate["2026-10-08"], null, "今天之后留空");
  assert.equal(weeks[2].month, 10);
});

test("calendar without any data still draws gray past days", () => {
  const cells = buildActivityCalendar("2026-10-07", [], 2).flatMap((week) => week.days);
  assert.equal(cells.filter((cell) => cell.count === 0).length, 10);
  assert.equal(cells.filter((cell) => cell.count === null).length, 4);
});

test("timezone note only when browser and server differ", () => {
  assert.equal(activityTimezoneNote("+08:00", "钟点", 480), "");
  assert.match(activityTimezoneNote("+08:00", "钟点", -300), /UTC-05:00/);
});
