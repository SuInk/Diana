// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"strings"
	"testing"
	"time"
)

// QQ 头像 CDN 回 max-age=2592000，地址又是固定的，换了头像控制台里能挂一个月。
func TestFreshAvatarURLBustsTheCDNCacheHourly(t *testing.T) {
	hour := time.Now().UTC().Format("2006010215")

	group := freshAvatarURL("https://p.qlogo.cn/gh/20005/20005/640")
	if !strings.HasSuffix(group, "?t="+hour) {
		t.Fatalf("群头像地址没带小时参数：%q", group)
	}
	// 成员头像地址本来就带查询串，不能再来一个问号。
	member := freshAvatarURL("https://q1.qlogo.cn/g?b=qq&nk=30007&s=640")
	if !strings.HasSuffix(member, "&t="+hour) || strings.Count(member, "?") != 1 {
		t.Fatalf("成员头像地址拼坏了：%q", member)
	}
	// 同一小时内必须是同一条地址，否则浏览器每次刷新都要重下一遍全部头像。
	if freshAvatarURL("https://q1.qlogo.cn/g?nk=1") != freshAvatarURL("https://q1.qlogo.cn/g?nk=1") {
		t.Fatal("同一小时内地址不稳定")
	}
	// 走本机代理的地址（Telegram）由我们自己发 Cache-Control，不用加参数。
	if got := freshAvatarURL("/api/assistant/groups/-1001/avatar"); got != "/api/assistant/groups/-1001/avatar" {
		t.Fatalf("本机代理地址被改了：%q", got)
	}
	if got := freshAvatarURL(""); got != "" {
		t.Fatalf("空地址被塞了参数：%q", got)
	}
}
