import assert from "node:assert/strict";
import test from "node:test";
import {
  sortWorkspaceAreas,
  workspaceCanDelete,
  workspaceDownloadURL,
  workspaceEntryLabel,
  workspaceIsEmpty,
  workspaceQuotaPercent
} from "./workspace-files.ts";

const area = (key, extra = {}) => ({ key, label: key, path: key, retention: "", bytes: 0, files: 0, entries: [], ...extra });

test("下载链接把路径整段编码，空格、中文和 & 都不会截断查询串", () => {
  assert.equal(
    workspaceDownloadURL("downloads/季度 报告&附件.pdf"),
    "/api/workspace/download?path=downloads%2F%E5%AD%A3%E5%BA%A6%20%E6%8A%A5%E5%91%8A%26%E9%99%84%E4%BB%B6.pdf"
  );
});

test("分区排序：长期保存区在前、回收站和其它垫底，不认识的区排最后", () => {
  const sorted = sortWorkspaceAreas([
    area("other"),
    area("trash"),
    area("future"),
    area("downloads"),
    area("keep", { bot_id: "b2", bot_name: "小二" }),
    area("keep", { bot_id: "b1", bot_name: "阿一" }),
    area("tmp")
  ]);
  assert.deepEqual(
    sorted.map((item) => item.bot_id ?? item.key),
    ["b1", "b2", "downloads", "tmp", "trash", "other", "future"]
  );
  assert.deepEqual(sortWorkspaceAreas(undefined), []);
});

test("回收站里的条目不给单条删除", () => {
  assert.equal(workspaceCanDelete(area("trash")), false);
  assert.equal(workspaceCanDelete(area("downloads")), true);
});

test("配额百分比夹在 0–100，没有配额不画条", () => {
  assert.equal(workspaceQuotaPercent(50, 200), 25);
  assert.equal(workspaceQuotaPercent(500, 200), 100);
  assert.equal(workspaceQuotaPercent(50, 0), null);
  assert.equal(workspaceQuotaPercent(50, undefined), null);
});

test("空工作目录：分区都没文件且没有散落和闲置项", () => {
  const empty = { root: "/w", collected_at: "", areas: [area("keep"), area("tmp")], loose: [], orphan_coding: [] };
  assert.equal(workspaceIsEmpty(null), true);
  assert.equal(workspaceIsEmpty(empty), true);
  assert.equal(workspaceIsEmpty({ ...empty, loose: [{ path: "a.txt", name: "a.txt", size: 1, modified: "" }] }), false);
  assert.equal(workspaceIsEmpty({ ...empty, areas: [area("tmp", { files: 1 })] }), false);
});

test("条目显示分区内的相对路径，不在分区目录下的原样显示", () => {
  const keep = area("keep", { path: "keep/bot-1/" });
  assert.equal(workspaceEntryLabel(keep, { path: "keep/bot-1/notes/plan.md", name: "plan.md" }), "notes/plan.md");
  assert.equal(workspaceEntryLabel(keep, { path: "keep/bot-10/plan.md", name: "plan.md" }), "keep/bot-10/plan.md");
  assert.equal(workspaceEntryLabel(area("other", { path: "" }), { path: "misc/a.txt", name: "a.txt" }), "misc/a.txt");
});
