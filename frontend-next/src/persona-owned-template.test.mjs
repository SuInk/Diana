import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import { personaOwnedTemplate } from "./persona-owned-template.ts";

// 切到接管模式之后，运行时就不再补「怎么说话」那几段了。模板是用户的起点，里面
// 漏掉哪一项，那一项就两头都没有：正文没写，运行时也不给。段头只是给人看的结构，
// 运行时不解析它——所以这里查的是内容确实谈到了这几件事，不是段头拼写。
test("模板覆盖了接管之后没人再管的那几件事", () => {
  for (const topic of ["自称", "语气词", "动作描写", "表情符号", "答多长", "聊天节奏"]) {
    assert.ok(personaOwnedTemplate.includes(topic), `模板没谈到「${topic}」，切过去之后这件事两头都没有`);
  }
});

// 和后端 PersonaOwnedTemplate 逐字节对照。这套测试也会在 Docker 的 frontend-next
// 构建阶段跑（npm 的 prebuild），那一层只有 frontend-next/ 一个目录、读不到 .go 源码，
// 所以按文件存在与否跳过，本地和 CI 里照常比。
test("和后端那份逐字节一致", { skip: !tryRead() }, () => {
  const literal = tryRead().match(/^const PersonaOwnedTemplate = (".*")$/m);
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
