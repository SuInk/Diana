import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { parse } from "@vue/compiler-sfc";

const script = parse(readFileSync(new URL("./views/SetupWizard.vue", import.meta.url), "utf8")).descriptor.scriptSetup.content;
const ast = ts.createSourceFile("view.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);

function load(name, context) {
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name);
  assert.ok(fn, `missing ${name}`);
  vm.runInContext(ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  return context[name];
}

// 向导第二步的表单：默认是空白的新配置，每个用例只填自己关心的那几格。
function draft(overrides) {
  return {
    platform: "onebot-v11",
    onebot_transport: "reverse_ws", onebot_ws_endpoint: "", onebot_http_url: "", onebot_http_secret: "",
    onebot_reverse_ws_endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", owner_id: "", onebot_access_token: "",
    telegram_bot_token: "", telegram_api_base_url: "", telegram_proxy_url: "",
    qq_app_id: "", qq_app_secret: "", qq_sandbox: false,
    dingtalk_client_id: "", dingtalk_client_secret: "", dingtalk_robot_code: "",
    feishu_app_id: "", feishu_app_secret: "", feishu_verification_token: "", feishu_encrypt_key: "", feishu_api_base_url: "",
    wecom_corp_id: "", wecom_agent_id: "", wecom_secret: "", wecom_token: "", wecom_encoding_aes_key: "",
    imessage_server_url: "", imessage_password: "", imessage_webhook_token: "",
    ...overrides
  };
}

function wizard(form, saved = {}) {
  const botForm = { value: form };
  const isOneBot = form.platform === "onebot-v11";
  const context = vm.createContext({
    URL, botForm, savedBot: { value: saved },
    isOneBotPlatform: { value: isOneBot },
    wsEndpoint: { value: form.onebot_reverse_ws_endpoint.trim() },
    tokenRequired: { value: isOneBot && form.onebot_transport === "reverse_ws" && !saved.onebot_access_token_configured }
  });
  load("validWebSocketURL", context);
  load("secretConfigured", context);
  return { credentialError: load("credentialError", context), platformPayload: load("platformPayload", context) };
}

test("each platform asks for its own credentials, not OneBot's", () => {
  assert.match(wizard(draft({ platform: "telegram" })).credentialError(), /Bot Token/);
  assert.match(wizard(draft({ platform: "qq-official" })).credentialError(), /AppID/);
  assert.match(wizard(draft({ platform: "dingtalk" })).credentialError(), /Client ID/);
  assert.match(wizard(draft({ platform: "feishu" })).credentialError(), /App ID/);
  assert.match(wizard(draft({ platform: "wecom" })).credentialError(), /企业 ID/);
  assert.match(wizard(draft({ platform: "imessage" })).credentialError(), /BlueBubbles 服务器地址/);
  assert.match(wizard(draft({ platform: "imessage", imessage_server_url: "http://mac.local:1234" })).credentialError(), /密码/);
  // 填全即可保存，不因为没填 OneBot 的回连地址或 token 被拦下。
  assert.equal(wizard(draft({ platform: "telegram", telegram_bot_token: "123:abc" })).credentialError(), "");
  assert.equal(wizard(draft({ platform: "dingtalk", dingtalk_client_id: "ding", dingtalk_client_secret: "s" })).credentialError(), "");
  assert.equal(wizard(draft({ platform: "imessage", imessage_server_url: "http://mac.local:1234", imessage_password: "p" })).credentialError(), "");
});

// 第二次进向导时密钥不回显，留空表示沿用；校验必须认 *_configured，否则配好的
// 机器人再打开向导就会被自己的校验拦住。
test("already saved secrets satisfy the check when the field is left blank", () => {
  assert.equal(wizard(draft({ platform: "telegram" }), { telegram_bot_token_configured: true }).credentialError(), "");
  assert.equal(
    wizard(draft({ platform: "wecom", wecom_corp_id: "ww1", wecom_agent_id: "1000002" }), {
      wecom_secret_configured: true, wecom_token_configured: true, wecom_encoding_aes_key_configured: true
    }).credentialError(),
    ""
  );
});

test("WeCom needs a numeric agent id and both callback keys", () => {
  const filled = { platform: "wecom", wecom_corp_id: "ww1", wecom_agent_id: "1000002", wecom_secret: "s", wecom_token: "t", wecom_encoding_aes_key: "k" };
  assert.equal(wizard(draft(filled)).credentialError(), "");
  assert.match(wizard(draft({ ...filled, wecom_agent_id: "agent" })).credentialError(), /AgentId/);
  // 只能发不能收的半可用状态在这里拦下，而不是等消息收不到再排查。
  assert.match(wizard(draft({ ...filled, wecom_encoding_aes_key: "" })).credentialError(), /EncodingAESKey/);
});

test("OneBot keeps its endpoint and reverse-WS token rules", () => {
  assert.match(wizard(draft({ onebot_reverse_ws_endpoint: "http://127.0.0.1:18080" })).credentialError(), /ws:\/\//);
  assert.match(wizard(draft()).credentialError(), /Access Token/);
  assert.equal(wizard(draft({ onebot_access_token: "0123456789abcdef" })).credentialError(), "");
  assert.match(wizard(draft({ onebot_transport: "http", onebot_http_url: "127.0.0.1:5700" })).credentialError(), /http:\/\//);
  assert.match(wizard(draft({ onebot_transport: "http", onebot_http_url: "http://127.0.0.1:5700" })).credentialError(), /签名密钥/);
});

// 换平台保存时只提交这一个平台的接入字段：另一套接入信息跟着已存配置走，不被清空。
test("saving one platform does not touch the other platforms' fields", () => {
  const telegram = wizard(draft({ platform: "telegram", telegram_bot_token: "123:abc", telegram_proxy_url: " http://127.0.0.1:7890 " })).platformPayload();
  assert.deepEqual({ ...telegram }, { telegram_bot_token: "123:abc", telegram_api_base_url: "", telegram_proxy_url: "http://127.0.0.1:7890" });
  assert.equal("onebot_reverse_ws_endpoint" in telegram, false);
  assert.equal("onebot_access_token" in telegram, false);

  const onebot = wizard(draft({ onebot_access_token: "0123456789abcdef" })).platformPayload();
  assert.equal(onebot.onebot_reverse_ws_endpoint, "ws://127.0.0.1:18080/onebot/v11/ws");
  assert.equal("telegram_bot_token" in onebot, false);

  // 留空的密钥不提交 undefined 以外的值，后端据此沿用已存的那份。
  const feishu = wizard(draft({ platform: "feishu", feishu_app_id: "cli_1" }), { feishu_app_secret_configured: true }).platformPayload();
  assert.equal(feishu.feishu_app_secret, undefined);
  assert.equal(feishu.feishu_app_id, "cli_1");
});
