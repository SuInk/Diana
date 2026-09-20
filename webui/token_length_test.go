// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import "testing"

// 反向 WebSocket 的监听器只绑在本机，token 防的是同机上的其他进程冒连。门槛按 8 位
// 定：既拦得住手滑写的短口令，也不会把既有的、够用的 token 挡在外面——16 位那一版
// 上线后，线上一个 15 位的 token 就再也存不回去了。
func TestValidateTokenLength(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		ok    bool
	}{
		{"留空表示不鉴权或沿用旧值", "", true},
		{"8 位刚好通过", "abcd1234", true},
		{"7 位不够", "abcd123", false},
		{"既有的 15 位 token 仍然可以保存", "abcdefghij12345", true},
		{"一键生成的 32 位", "aB3dEfGhIjKlMnOpQrStUvWxYz012345", true},
		// 长度按字符数算，不是字节数：中文 token 不该因为占 3 字节就被放行。
		{"7 个汉字不够", "口令口令口令口", false},
		{"8 个汉字通过", "口令口令口令口令", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTokenLength("onebot_access_token", tc.value)
			if tc.ok && err != nil {
				t.Fatalf("应当通过，却报错：%v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("应当被拒绝，却通过了")
			}
		})
	}
}
