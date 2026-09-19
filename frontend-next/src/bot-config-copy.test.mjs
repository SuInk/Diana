import assert from "node:assert/strict";
import { test } from "node:test";
import { copyBotConfiguration } from "./bot-config-copy.ts";

const defaults = { platform: "onebot-v11", enabled: true, onebot_transport: "reverse_ws", onebot_reverse_ws_endpoint: "ws://localhost/onebot/v11/ws" };

test("copy keeps behavior and nested model/persona drafts independent", () => {
  const source = { ...defaults, id: "source", name: "助手", enabled: false, owner_id: "42", system_prompt: "persona", group_triggers: ["助手"], private_admission: { mode: "whitelist", allowed_users: ["42"] }, model_roles: { chat: { profile_id: "provider", model: "chat", fallbacks: [{ profile_id: "backup", model: "small" }] } }, custom_persona: { system_prompt: "custom" }, telegram_suppress_bot_messages: false };
  const draft = copyBotConfiguration(source, defaults);
  assert.equal(draft.name, "助手 副本");
  assert.equal(draft.owner_id, "42");
  assert.equal(draft.system_prompt, source.system_prompt);
  assert.deepEqual(draft.model_roles, source.model_roles);
  assert.deepEqual(draft.private_admission, source.private_admission);
  assert.equal(draft.telegram_suppress_bot_messages, false);
  draft.group_triggers.push("new");
  draft.model_roles.chat.fallbacks[0].model = "edited";
  draft.custom_persona.system_prompt = "edited";
  assert.deepEqual(source.group_triggers, ["助手"]);
  assert.equal(source.model_roles.chat.fallbacks[0].model, "small");
  assert.equal(source.custom_persona.system_prompt, "custom");
});

test("copy clears identity, topology, all platform credentials and their saved indicators", () => {
  const source = { ...defaults, id: "source", name: "Source", bot_account: "old-account", avatar_url: "old-avatar", active_profile_id: "source", profiles: [{ id: "source" }], message_relays: [{ id: "relay" }], connection_profile_id: "root", nonebot_bridge_enabled: true };
  const credentials = ["onebot_access_token", "onebot_http_secret", "telegram_bot_token", "qq_app_secret", "dingtalk_client_secret", "feishu_app_secret", "feishu_verification_token", "feishu_encrypt_key", "wecom_secret", "wecom_token", "wecom_encoding_aes_key", "nonebot_bridge_token"];
  for (const field of credentials) {
    source[field] = "secret";
    source[`${field}_configured`] = true;
    source[`${field}_preview`] = "masked";
  }
  Object.assign(source, { qq_app_id: "old", dingtalk_client_id: "old", feishu_app_id: "old", wecom_corp_id: "old", onebot_ws_endpoint: "ws://old", telegram_proxy_url: "http://user:password@proxy" });
  const draft = copyBotConfiguration(source, defaults);
  for (const field of credentials) {
    assert.equal(draft[field], undefined, field);
    assert.equal(draft[`${field}_configured`], undefined, field);
    assert.equal(draft[`${field}_preview`], undefined, field);
    assert.equal(source[field], "secret", "source was modified");
  }
  for (const field of ["id", "bot_account", "avatar_url", "active_profile_id", "profiles", "message_relays", "qq_app_id", "dingtalk_client_id", "feishu_app_id", "wecom_corp_id", "onebot_ws_endpoint", "telegram_proxy_url"]) assert.equal(draft[field], undefined, field);
  assert.equal(draft.connection_profile_id, "");
  assert.equal(draft.onebot_reverse_ws_endpoint, defaults.onebot_reverse_ws_endpoint);
  assert.equal(draft.nonebot_bridge_enabled, false);
});
