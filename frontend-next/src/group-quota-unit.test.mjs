import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const source = readFileSync(new URL("./views/GroupsView.vue", import.meta.url), "utf8");
const script = parse(source).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const context = vm.createContext({});
for (const name of ["parseTokenQuota", "formatTokenQuota"]) {
  const fn = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name);
  assert.ok(fn, `missing ${name}`);
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
}

// 裸数字按 K 算：写额度的人心里想的是「五十万」，不是五位数一个个数零。
test("bare numbers default to K", () => {
  assert.equal(context.parseTokenQuota("500"), 500_000);
  assert.equal(context.parseTokenQuota("1.5"), 1_500);
  assert.equal(context.parseTokenQuota(" 2 000 "), 2_000_000);
});

test("explicit units win over the default", () => {
  assert.equal(context.parseTokenQuota("500k"), 500_000);
  assert.equal(context.parseTokenQuota("1.5m"), 1_500_000);
  assert.equal(context.parseTokenQuota("50万"), 500_000);
  assert.equal(context.parseTokenQuota("50w"), 500_000);
  // 想按原始 token 数填的人也有：token / t 表示不乘。
  assert.equal(context.parseTokenQuota("8000token"), 8_000);
  assert.equal(context.parseTokenQuota("8000t"), 8_000);
});

test("empty and unparsable input are distinguishable", () => {
  assert.equal(context.parseTokenQuota(""), undefined);
  assert.equal(context.parseTokenQuota("   "), undefined);
  assert.equal(context.parseTokenQuota("五十万"), undefined);
  assert.equal(context.parseTokenQuota("500kk"), undefined);
});

// 存的是精确 token 数，回填时要还原成人写得出来的样子。
test("round-trips through the K-based draft", () => {
  assert.equal(context.formatTokenQuota(500_000), "500");
  assert.equal(context.formatTokenQuota(1_500_000), "1500");
  assert.equal(context.formatTokenQuota(0), "");
  assert.equal(context.formatTokenQuota(undefined), "");
  // 不是整千的值不能被 K 抹掉，原样带单位写出来。
  assert.equal(context.formatTokenQuota(8_192), "8192token");
  assert.equal(context.parseTokenQuota(context.formatTokenQuota(8_192)), 8_192);
  assert.equal(context.parseTokenQuota(context.formatTokenQuota(500_000)), 500_000);
});
