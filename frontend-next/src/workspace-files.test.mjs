import assert from "node:assert/strict";
import test from "node:test";
import {
  sortWorkspaceAreas,
  workspaceAreaTarget,
  workspaceCanDelete,
  workspaceInTrash,
  workspacePreviewKind,
  workspaceProtectedLabel,
  workspaceQuotaPercent
} from "./workspace-files.ts";

const area = (key, extra = {}) => ({ key, label: key, path: key, retention: "", bytes: 0, files: 0, ...extra });

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

test("配额百分比夹在 0–100，没有配额不画条", () => {
  assert.equal(workspaceQuotaPercent(50, 200), 25);
  assert.equal(workspaceQuotaPercent(500, 200), 100);
  assert.equal(workspaceQuotaPercent(50, 0), null);
  assert.equal(workspaceQuotaPercent(50, undefined), null);
});

test("分区卡片跳到的目录：「其他目录」是根，点开头的目录名原样保留", () => {
  assert.equal(workspaceAreaTarget({ path: "." }), "");
  assert.equal(workspaceAreaTarget({ path: ".trash" }), ".trash");
  assert.equal(workspaceAreaTarget({ path: ".agent-browser/" }), ".agent-browser");
  assert.equal(workspaceAreaTarget({ path: "keep/bot-a" }), "keep/bot-a");
});

test("回收站里不给单条删除", () => {
  assert.equal(workspaceInTrash(".trash"), true);
  assert.equal(workspaceInTrash(".trash/20260901-000000/a.txt"), true);
  assert.equal(workspaceInTrash(".trashy/a.txt"), false);
  assert.equal(workspaceCanDelete({ path: ".trash/20260901-000000/a.txt", kind: "file" }), false);
});

test("能删：普通文件、子目录、链接和 .diana/ 里的文件；不能删：分区根、机器人长期区目录", () => {
  assert.equal(workspaceCanDelete({ path: "downloads/a.png", kind: "file" }), true);
  assert.equal(workspaceCanDelete({ path: "downloads/batch", kind: "dir" }), true);
  assert.equal(workspaceCanDelete({ path: "keep/bot-a/poster.png", kind: "file" }), true);
  assert.equal(workspaceCanDelete({ path: "notes", kind: "dir" }), true);
  assert.equal(workspaceCanDelete({ path: "broken", kind: "link" }), true);
  assert.equal(workspaceCanDelete({ path: "outside", kind: "dir", external: true }), true);
  assert.equal(workspaceCanDelete({ path: ".diana/keep-index/bot-a.json", kind: "file" }), true);
  assert.equal(workspaceCanDelete({ path: ".mcp.json", kind: "file" }), true);
  for (const path of ["downloads", "keep", ".trash", "coding", ".agent-browser", ".diana"]) {
    assert.equal(workspaceCanDelete({ path, kind: "dir" }), false, path);
  }
  assert.equal(workspaceCanDelete({ path: "keep/bot-a", kind: "dir" }), false);
});

test("经外部链接进到工作区外面的目录里，什么都不给删", () => {
  assert.equal(workspaceCanDelete({ path: "host-logs/diana.log", kind: "file" }, true), false);
  assert.equal(workspaceCanDelete({ path: "host-logs/diana.log", kind: "file" }, false), true);
});

test("受保护条目的标记：.diana/ 和老位置的扩展开关是运行时文件，其余是凭据", () => {
  assert.equal(workspaceProtectedLabel({ path: "notes.md" }), "");
  assert.equal(workspaceProtectedLabel({ path: ".diana", protected: true }), "运行时文件");
  assert.equal(workspaceProtectedLabel({ path: ".diana/keep-index/bot-a.json", protected: true }), "运行时文件");
  assert.equal(workspaceProtectedLabel({ path: ".extension-overrides.json", protected: true }), "运行时文件");
  assert.equal(workspaceProtectedLabel({ path: ".mcp.json", protected: true }), "凭据");
  assert.equal(workspaceProtectedLabel({ path: "coding-runtime/auth", protected: true }), "凭据");
});

test("预览方式按扩展名认，认不出的先当文本", () => {
  assert.equal(workspacePreviewKind("a.PNG"), "image");
  assert.equal(workspacePreviewKind("clip.mov"), "video");
  assert.equal(workspacePreviewKind("song.flac"), "audio");
  assert.equal(workspacePreviewKind("doc.pdf"), "pdf");
  assert.equal(workspacePreviewKind("notes.md"), "text");
  assert.equal(workspacePreviewKind(".env"), "text");
  assert.equal(workspacePreviewKind("README"), "text");
});
