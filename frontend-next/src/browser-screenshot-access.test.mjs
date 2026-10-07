import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { computed, ref } from "vue";

function panelHarness(api) {
  const botScope = ref("bot-a");
  const source = readFileSync(new URL("./components/AgentBrowserPanel.vue", import.meta.url), "utf8");
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
  const compiled = ts.transpileModule(script + "\nexport { load, save, screenshotAccess, screenshotAccessOptions, operationAccess, loadError, formValid, screenshotWithoutOperation, screenshotUsersWithoutOperation };", {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 }
  }).outputText;
  const context = vm.createContext({
    exports: {},
    defineEmits: () => () => {},
    Error,
    URL,
    require: name => {
      if (name === "vue") return { ref, computed, watch() {}, onMounted() {} };
      if (name === "../bot-scope") return { botScope };
      if (name === "../api") return api;
      if (name === "../toast") return { toastSuccess() {}, toastError() {} };
      throw new Error(`unexpected import ${name}`);
    }
  });
  vm.runInContext(compiled, context);
  return { ...context.exports, botScope };
}

const plain = value => JSON.parse(JSON.stringify(value));

test("browser panel defaults to owner and saves the selected mode with its user IDs", async () => {
  const saves = [];
  const panel = panelHarness({
    getAgentBrowser: async () => ({ cdp_url: "http://127.0.0.1:9222", timeout_ms: 15000, tools: ["browser_screenshot"] }),
    saveAgentBrowser: async (profile, url, timeout, access) => {
      saves.push({ profile, access: plain(access) });
      return { cdp_url: url, timeout_ms: timeout, screenshot_access: plain(access) };
    }
  });
  await panel.load();
  assert.deepEqual(plain(panel.screenshotAccess.value), { mode: "owner_only", allowed_users: [], allowed_hosts: [], allowed_groups: [] });
  panel.screenshotAccess.value = { mode: "whitelist", allowed_users: ["1001", "1002"], allowed_hosts: ["example.com"], allowed_groups: ["1234"] };
  await panel.save();
  assert.deepEqual(saves, [{ profile: "bot-a", access: { mode: "whitelist", allowed_users: ["1001", "1002"], allowed_hosts: ["example.com"], allowed_groups: ["1234"] } }]);
  panel.screenshotAccess.value.mode = "disabled";
  await panel.save();
  assert.equal(saves[1].access.mode, "disabled");
});

test("switching bots cannot apply another bot's policy when the new read fails", async () => {
  let saves = 0;
  const panel = panelHarness({
    getAgentBrowser: async profile => {
      if (profile === "bot-b") throw new Error("read failed");
      return { screenshot_access: { mode: "whitelist" } };
    },
    saveAgentBrowser: async () => { saves += 1; return {}; }
  });
  await panel.load();
  panel.botScope.value = "bot-b";
  await panel.load();
  await panel.save();
  assert.equal(saves, 0);
  assert.equal(panel.loadError.value, "read failed");
});

test("a save response for the previous bot cannot overwrite the current bot's policy", async () => {
  let finish;
  const panel = panelHarness({
    getAgentBrowser: async profile => ({ screenshot_access: profile === "bot-a" ? { mode: "whitelist", allowed_users: ["1001"], allowed_hosts: ["example.com"] } : { mode: "disabled" } }),
    saveAgentBrowser: async () => new Promise(resolve => { finish = resolve; })
  });
  await panel.load();
  const saving = panel.save();
  panel.botScope.value = "bot-b";
  await panel.load();
  finish({ screenshot_access: { mode: "whitelist" } });
  await saving;
  assert.equal(panel.screenshotAccess.value.mode, "disabled");
});

 test("login screenshot modes omit everyone and legacy all falls back to owner", async () => {
 const panel = panelHarness({getAgentBrowser: async () => ({ screenshot_access: {mode:"all", allowed_users:["1"]} })});
 await panel.load();
 assert.deepEqual(plain(panel.screenshotAccessOptions).map(item => item.value), ["disabled", "owner_only", "whitelist"]);
 assert.equal(panel.screenshotAccess.value.mode, "owner_only");
 });

 test("personal operation allowlist loads, saves and never selects another user's profile", async () => {
  const policy = {mode:"whitelist",allowed_users:["1001"],allowed_hosts:["example.com","login.example.com"]};
  let saved;
  const panel = panelHarness({
   getAgentBrowser: async () => ({operation_access:policy}),
   saveAgentBrowser: async (profile,url,timeout,screenshots,operations) => { saved = {profile,operations:plain(operations)}; return {operation_access:plain(operations)}; }
  });
  await panel.load();
  assert.deepEqual(plain(panel.operationAccess.value),policy);
  await panel.save();
  assert.deepEqual(saved,{profile:"bot-a",operations:policy});
  assert.equal("user_id" in saved,false);
 });

test("an incomplete or malformed allowlist blocks saving instead of silently granting nothing", async () => {
  let saves = 0;
  const panel = panelHarness({
    getAgentBrowser: async () => ({}),
    saveAgentBrowser: async () => { saves += 1; return {}; }
  });
  await panel.load();
  panel.operationAccess.value = { mode: "whitelist", allowed_users: ["1001"], allowed_hosts: [] };
  assert.equal(panel.formValid.value, false);
  await panel.save();
  panel.operationAccess.value.allowed_hosts = ["https://example.com/login"];
  assert.equal(panel.formValid.value, false);
  panel.operationAccess.value.allowed_hosts = ["example.com:8443"];
  panel.screenshotAccess.value = { mode: "whitelist", allowed_users: [], allowed_hosts: ["example.com"], allowed_groups: [] };
  assert.equal(panel.formValid.value, false);
  await panel.save();
  assert.equal(saves, 0);
  panel.screenshotAccess.value.allowed_users = ["1001"];
  assert.equal(panel.formValid.value, true);
  await panel.save();
  assert.equal(saves, 1);
});

test("screenshot grants that the operation policy cannot honour are flagged", async () => {
  const panel = panelHarness({ getAgentBrowser: async () => ({}) });
  await panel.load();
  panel.screenshotAccess.value = { mode: "whitelist", allowed_users: ["1001", "1002"], allowed_hosts: ["example.com"], allowed_groups: [] };
  assert.equal(panel.screenshotWithoutOperation.value, true);
  panel.operationAccess.value = { mode: "whitelist", allowed_users: ["1001"], allowed_hosts: ["example.com"] };
  assert.equal(panel.screenshotWithoutOperation.value, false);
  assert.deepEqual(plain(panel.screenshotUsersWithoutOperation.value), ["1002"]);
});
