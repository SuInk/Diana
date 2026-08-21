// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 配置键从带平台名的旧写法换成中性写法后，存量数据库里存的仍是旧键。
// 读不回来的话，升级之后机器人账号和脱敏开关会凭空丢失。
func TestConfigReadsLegacyPlatformKeys(t *testing.T) {
	legacy := []byte(`{"bot_qq":"10001","llm_qq_id_masking_enabled":false,"owner_id":"20002"}`)

	var cfg BotConfig
	if err := json.Unmarshal(legacy, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BotAccount != "10001" || cfg.OwnerID != "20002" {
		t.Fatalf("legacy bot config = %#v", cfg)
	}
	if cfg.LLMIdentityMaskingEnabled == nil || *cfg.LLMIdentityMaskingEnabled {
		t.Fatalf("legacy masking flag lost: %#v", cfg.LLMIdentityMaskingEnabled)
	}

	var payload ConfigPayload
	if err := json.Unmarshal(legacy, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BotAccount != "10001" {
		t.Fatalf("legacy payload = %#v", payload)
	}

	// 新键优先，不会被旧键覆盖。
	both := []byte(`{"bot_account":"30003","bot_qq":"10001"}`)
	var mixed BotConfig
	if err := json.Unmarshal(both, &mixed); err != nil {
		t.Fatal(err)
	}
	if mixed.BotAccount != "30003" {
		t.Fatalf("new key should win: %#v", mixed.BotAccount)
	}

	// 写出去用新键。
	encoded, err := json.Marshal(BotConfig{BotAccount: "40004"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"bot_account":"40004"`) || strings.Contains(string(encoded), "bot_qq") {
		t.Fatalf("encoded = %s", encoded)
	}
}
