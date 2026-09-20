import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import { personaOwnedTemplate } from "./persona-owned-template.ts";
import { personaOwnedHeaders } from "./persona-owned.ts";

// 模板要带齐界面会说「已接管」的那几个段头，否则用户切到接管模式、照模板改完，
// 界面说控件已停用，而运行时其实还在补那一段——两边说的不是同一件事。
test("模板带齐所有带控件的段头", () => {
  for (const header of Object.values(personaOwnedHeaders)) {
    assert.ok(personaOwnedTemplate.includes(header), `模板缺少段头 ${header}`);
  }
});

// 和后端 PersonaOwnedTemplate 逐字节对照。这套测试也会在 Docker 的 frontend-next
// 构建阶段跑（npm 的 prebuild），那一层只有 frontend-next/ 一个目录、读不到 .go 源码，
// 所以按文件存在与否跳过，本地和 CI 里照常比。
test("和后端那份逐字节一致", { skip: !tryRead() }, () => {
  const go = tryRead();
  const literal = go.match(/^const PersonaOwnedTemplate = (".*")$/m);
  assert.ok(literal, "types.go 里没找到 PersonaOwnedTemplate");
  assert.equal(JSON.parse(literal[1]), personaOwnedTemplate);
});

function tryRead() {
  try {
    return readFileSync(new URL("../../model/assistant/types.go", import.meta.url), "utf8");
  } catch {
    return "";
  }
}
