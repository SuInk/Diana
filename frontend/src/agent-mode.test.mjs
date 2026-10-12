import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  agentModeLabel,
  needsSafeModeConfirm,
  normalizeAgentMode,
  safeModeRuleLabel,
  safeModeSwitchConfirm
} from "./agent-mode.ts";

// 演示站那份目录由后端规则表生成，拿它当真实数据测，规则表一变这里也跟着变。
const catalog = JSON.parse(readFileSync(new URL("./demo-agent-safe-mode.json", import.meta.url), "utf8"));

test("mode normalization mirrors the backend migration", () => {
  assert.equal(normalizeAgentMode("standard"), "standard");
  assert.equal(normalizeAgentMode(" Safe "), "safe");
  assert.equal(normalizeAgentMode("", true), "standard");
  assert.equal(normalizeAgentMode(undefined), "standard");
  assert.equal(normalizeAgentMode(undefined, false), "safe");
  assert.equal(normalizeAgentMode("typo", false), "standard");
  assert.equal(agentModeLabel("safe"), "安全模式");
});

test("only switching into safe mode asks for confirmation", () => {
  assert.equal(needsSafeModeConfirm("standard", "safe"), true);
  assert.equal(needsSafeModeConfirm("safe", "standard"), false);
  assert.equal(needsSafeModeConfirm("safe", "safe"), false);
});

test("confirm dialog lists every category impact and the live situation", () => {
  const request = safeModeSwitchConfirm(catalog, { runningCodingJobs: 2, enabledMCPServers: 3, heldTasks: 4 });
  assert.equal(request.confirmLabel, "确认切换到安全模式");
  assert.equal(request.danger, true);
  for (const category of catalog) {
    assert.ok(request.message.includes(category.impact), category.id);
  }
  assert.match(request.message, /2 个编码任务正在运行：不会被中断/);
  assert.match(request.message, /3 个 MCP 服务/);
  assert.match(request.message, /4 个往当前会话以外发消息的任务.*暂停投递/);
  assert.match(request.message, /到点不发，任务保留/);
  assert.match(request.message, /包括主人本人/);
});

test("unknown or empty live impact is left out, and a missing catalog still warns", () => {
  const quiet = safeModeSwitchConfirm(catalog, { runningCodingJobs: 0, enabledMCPServers: null });
  assert.doesNotMatch(quiet.message, /当前情况/);
  const offline = safeModeSwitchConfirm([], null);
  assert.match(offline.message, /本机命令和编码代理停用/);
  assert.match(offline.message, /内置浏览器/);
});

test("rule labels name the tool and only the disabled operations", () => {
  const rules = catalog.flatMap((category) => category.rules);
  assert.equal(safeModeRuleLabel(rules.find((rule) => rule.tool === "run_command")), "run_command");
  assert.match(safeModeRuleLabel(rules.find((rule) => rule.tool === "platform")), /^platform（.*kick.*）$/);
});
