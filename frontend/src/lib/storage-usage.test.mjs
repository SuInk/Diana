import assert from "node:assert/strict";
import test from "node:test";
import {
  storageCategorySegments,
  storageDiskSegments,
  storageDiskTotal,
  storageDirectories,
  storageShareLabel,
  storageWidth
} from "./storage-usage.ts";

const usage = {
  collected_at: "2026-09-22T10:00:00Z",
  path: "/data",
  disk_total_bytes: 1000,
  disk_used_bytes: 600,
  disk_free_bytes: 400,
  disk_usage_percent: 60,
  diana_bytes: 200,
  diana_files: 3,
  categories: [
    { key: "video", label: "视频", bytes: 120, files: 1 },
    { key: "image", label: "图片", bytes: 80, files: 2 }
  ],
  scanning: false
};

test("饼图的三段拼满整块盘", () => {
  const segments = storageDiskSegments(usage);
  assert.deepEqual(
    segments.map((segment) => [segment.key, segment.bytes]),
    [["diana", 200], ["system", 400], ["free", 400]]
  );
  assert.equal(segments.reduce((sum, segment) => sum + segment.bytes, 0), storageDiskTotal(usage));
});

test("磁盘已用小于数据目录时不画出负扇区", () => {
  const segments = storageDiskSegments({ ...usage, disk_used_bytes: 100 });
  assert.equal(segments.find((segment) => segment.key === "system").bytes, 0);
});

test("读不到磁盘容量时饼图退回画分类，分母是数据目录体积", () => {
  const offline = { ...usage, disk_total_bytes: undefined, disk_used_bytes: undefined, disk_free_bytes: undefined };
  assert.deepEqual(storageDiskSegments(offline).map((segment) => segment.key), ["video", "image"]);
  assert.equal(storageDiskTotal(offline), 200);
});

test("分类段带固定色，未知分类回落到「其它文件」而不是没有颜色", () => {
  assert.equal(storageCategorySegments(usage)[0].color, "var(--storage-video)");
  const unknown = storageCategorySegments({ ...usage, categories: [{ key: "sticker", label: "表情包", bytes: 10, files: 1 }] });
  assert.equal(unknown[0].color, "var(--storage-other)");
});

test("占比标签：小份额不被四舍五入成 0%", () => {
  assert.equal(storageShareLabel(600, 1000), "60%");
  assert.equal(storageShareLabel(12, 1000), "1.2%");
  assert.equal(storageShareLabel(1, 100000), "<0.1%");
  assert.equal(storageShareLabel(0, 1000), "0.0%");
  assert.equal(storageShareLabel(10, 0), "—");
});

test("条宽永远是能用的 CSS 百分比", () => {
  assert.equal(storageWidth(600, 1000), "60%");
  assert.equal(storageWidth(1, 100000), "0.001%");
  assert.equal(storageWidth(0, 1000), "0%");
  assert.equal(storageWidth(10, 0), "0%");
  assert.equal(storageWidth(2000, 1000), "100%");
});

test("按目录拆分：旧后端没有字段时为空，空目录不占行，顺序沿用后端的体积倒序", () => {
  assert.deepEqual(storageDirectories(null), []);
  assert.deepEqual(storageDirectories(usage), []);
  const withDirectories = {
    ...usage,
    directories: [
      { key: "history-media", label: "历史媒体", bytes: 150, files: 2 },
      { key: "workspace/downloads", label: "工作目录 · 下载", bytes: 50, files: 1 },
      { key: "workspace/.trash", label: "工作目录 · 回收站", bytes: 0, files: 0 }
    ]
  };
  assert.deepEqual(
    storageDirectories(withDirectories).map((directory) => directory.key),
    ["history-media", "workspace/downloads"]
  );
});
