import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("总览支持长跨度与自定义日历筛选、多维用量导出以及错误直达日志和事件明细", async () => {
  const [dashboard, logs, events, breakdown, datePicker] = await Promise.all([
    readFile(new URL("../views/DashboardView.vue", import.meta.url), "utf8"),
    readFile(new URL("../views/LogsView.vue", import.meta.url), "utf8"),
    readFile(new URL("../views/EventsView.vue", import.meta.url), "utf8"),
    readFile(new URL("../components/TokenUsageBreakdown.vue", import.meta.url), "utf8"),
    readFile(new URL("../components/DateRangePicker.vue", import.meta.url), "utf8")
  ]);

  // 仪表盘长跨度与自定义日历筛选
  for (const rng of ['"today"', '"7d"', '"30d"', '"90d"', '"365d"', '"custom"']) {
    assert.ok(dashboard.includes(rng), `Dashboard missing range ${rng}`);
  }
  assert.match(dashboard, /getStatsOverview\(/);
  assert.match(dashboard, /<ActivityCalendar/);
  assert.match(dashboard, /<DateRangePicker/);
  assert.match(datePicker, /date-range-popover/);

  // 错误弹窗归并与直达日志/事件，以及 Token 弹窗 CSV 导出
  assert.match(dashboard, /inspectErrorItem/);
  assert.match(dashboard, /inspectErrorCategory/);
  assert.match(breakdown, /exportCSV/);
  assert.match(breakdown, /\\uFEFF/);

  // 日志页与事件页支持接收深链查询参数与目标记录高亮
  assert.match(logs, /params\.get\("kind"\)/);
  assert.match(logs, /params\.get\("status"\)/);
  assert.match(logs, /params\.get\("model"\)/);
  assert.match(logs, /params\.get\("id"\)/);
  assert.match(events, /params\.get\("result"\)/);
  assert.match(events, /params\.get\("event"\)/);
});
