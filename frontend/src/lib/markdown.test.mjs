import assert from "node:assert/strict";
import test from "node:test";

import { markdownToPlain, parseInline, parseMarkdown, safeHref } from "./markdown.ts";

test("inline markup becomes nodes and leaves snake_case alone", () => {
  assert.deepEqual(parseInline("**粗体** 和 *斜体*、`code`、~~删~~ group_id_name"), [
    { type: "strong", children: [{ type: "text", text: "粗体" }] },
    { type: "text", text: " 和 " },
    { type: "em", children: [{ type: "text", text: "斜体" }] },
    { type: "text", text: "、" },
    { type: "code", text: "code" },
    { type: "text", text: "、" },
    { type: "del", children: [{ type: "text", text: "删" }] },
    { type: "text", text: " group_id_name" },
  ]);
});

test("bare URLs stop before Chinese and trailing punctuation", () => {
  const nodes = parseInline("参考：https://example.com/a-b；另见 (https://example.org/x).");
  const links = nodes.filter((n) => n.type === "link").map((n) => n.href);
  assert.deepEqual(links, ["https://example.com/a-b", "https://example.org/x"]);
});

test("unsafe link targets stay plain text", () => {
  assert.deepEqual(parseInline("[点我](javascript:alert(1))").some((n) => n.type === "link"), false);
  assert.equal(safeHref("JavaScript:alert(1)"), false);
  assert.equal(safeHref("https://example.com"), true);
});

test("asterisks inside code are not emphasis and escapes are honored", () => {
  assert.deepEqual(parseInline("`a*b*c` \\*x\\*"), [
    { type: "code", text: "a*b*c" },
    { type: "text", text: " *x*" },
  ]);
});

test("blocks: heading, lists with nesting, code, quote, table", () => {
  const blocks = parseMarkdown([
    "## 结论",
    "1. **第一**：说明",
    "   - 子项",
    "2. 第二",
    "",
    "```go",
    "fmt.Println(\"**不是粗体**\")",
    "```",
    "> 引用",
    "",
    "| 群 | 数量 |",
    "|---|--:|",
    "| A | 3 |",
    "---",
    "最后一段",
    "第二行",
  ].join("\n"));
  assert.deepEqual(blocks.map((b) => b.type), ["heading", "list", "code", "quote", "table", "hr", "paragraph"]);
  const list = blocks[1];
  assert.equal(list.ordered, true);
  assert.equal(list.items.length, 2);
  assert.deepEqual(list.items[0].map((b) => b.type), ["paragraph", "list"]);
  assert.equal(blocks[2].lang, "go");
  assert.equal(blocks[2].text, "fmt.Println(\"**不是粗体**\")");
  assert.deepEqual(blocks[4].align, ["", "right"]);
  assert.deepEqual(blocks[6].children.map((n) => n.type), ["text", "br", "text"]);
});

test("list numbering keeps its start and HTML stays text", () => {
  const [list] = parseMarkdown("3. 三\n4. 四");
  assert.equal(list.start, 3);
  const [para] = parseMarkdown("<script>alert(1)</script>");
  assert.deepEqual(para.children, [{ type: "text", text: "<script>alert(1)</script>" }]);
});

test("plain preview drops markup", () => {
  assert.equal(markdownToPlain("好耶，**番茄牛腩**炖一锅\n- `a`\n- [链接](https://example.com)"), "好耶，番茄牛腩炖一锅 a 链接");
});
