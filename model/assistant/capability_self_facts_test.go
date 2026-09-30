// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestCapabilitySelfFactContextTriggers(t *testing.T) {
	hits := []string{
		"这玩意和 napcat 啥区别来着",
		"这个在snowluma那里可以设置吧",
		"给一下 NapCat 配置教程",
		"那怎么登陆的QQ",
		"diana 接 QQ 怎么部署",
		"OneBot 反向 ws 填什么",
	}
	for _, text := range hits {
		if got := capabilitySelfFactContext(text); !strings.Contains(got, "SnowLuma") {
			t.Fatalf("%q should inject the QQ client fact, got %q", text, got)
		}
	}
	misses := []string{
		"",
		"QQ 音乐会员过期了",
		"服务器部署好了吗",
		"内存条又涨价了",
	}
	for _, text := range misses {
		if got := capabilitySelfFactContext(text); got != "" {
			t.Fatalf("%q should not inject anything, got %q", text, got)
		}
	}
}

// 面向用户和模型的文字只推荐 SnowLuma；触发词里可以有别的客户端名，内容里不行。
func TestCapabilityDocumentsDoNotNameOtherQQClients(t *testing.T) {
	for _, document := range coreCapabilityDocuments {
		lower := strings.ToLower(document.Title + document.Content)
		for _, name := range []string{"napcat", "llonebot", "lagrange", "go-cqhttp"} {
			if strings.Contains(lower, name) {
				t.Fatalf("%s names %s", document.ID, name)
			}
		}
	}
}
