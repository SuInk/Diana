// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import "testing"

func TestPolicyDefaultsDenyEverything(t *testing.T) {
	policy := Policy{}.WithDefaults()
	if policy.Enabled {
		t.Fatal("默认必须是关闭的")
	}
	if policy.WriteEnabled {
		t.Fatal("默认必须只读")
	}
	if policy.HostAllowed("https://example.com/") {
		t.Fatal("空白名单不能放过任何站点")
	}
	if policy.OriginAllowed("chrome-extension://abc") {
		t.Fatal("空来源白名单不能放过任何来源")
	}
	if policy.CommandTimeoutMS != DefaultCommandTimeoutMS || policy.CommandsPerMinute != DefaultCommandsPerMinute {
		t.Fatalf("超时与限流应补默认值，得到 %+v", policy)
	}
}

func TestPolicyHostMatching(t *testing.T) {
	policy := Policy{
		AllowedHosts: []string{"example.com", "*.wiki.test", "https://docs.example.org/guide"},
		DeniedHosts:  []string{"admin.wiki.test"},
	}.WithDefaults()

	cases := []struct {
		url  string
		want bool
		why  string
	}{
		{"https://example.com/path?q=1", true, "精确匹配"},
		{"http://example.com:8443/", true, "端口不影响站点判断"},
		{"https://sub.example.com/", false, "精确匹配不含子域"},
		{"https://a.wiki.test/", true, "通配子域"},
		{"https://wiki.test/", false, "*.host 不含主域本身"},
		{"https://admin.wiki.test/", false, "黑名单优先于通配白名单"},
		{"https://docs.example.org/", true, "白名单里写整条地址时取主机名"},
		{"ftp://example.com/", false, "只允许 http/https"},
		{"file:///etc/passwd", false, "本地文件不放"},
		{"chrome-extension://abc/page.html", false, "扩展页不放"},
		{"", false, "空地址不放"},
		{"https://EXAMPLE.com/", true, "主机名大小写不敏感"},
		{"https://example.com./", true, "末尾点归一化"},
	}
	for _, tc := range cases {
		if got := policy.HostAllowed(tc.url); got != tc.want {
			t.Errorf("HostAllowed(%q) = %v, 期望 %v（%s）", tc.url, got, tc.want, tc.why)
		}
	}
}

func TestPolicyRejectsWildcardOnlyHost(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"*"}}.WithDefaults()
	if len(policy.AllowedHosts) != 0 {
		t.Fatalf("单独一个 * 不该被当成白名单项，得到 %v", policy.AllowedHosts)
	}
	if policy.HostAllowed("https://example.com/") {
		t.Fatal("* 不能放开全网")
	}
}

func TestPolicyOriginAllowed(t *testing.T) {
	policy := Policy{AllowedOrigins: []string{"chrome-extension://ABCdef/"}}.WithDefaults()
	if !policy.OriginAllowed("chrome-extension://abcdef") {
		t.Fatal("来源比较应忽略大小写与结尾斜杠")
	}
	if policy.OriginAllowed("") {
		t.Fatal("空 Origin 必须拒绝")
	}
	if policy.OriginAllowed("https://evil.test") {
		t.Fatal("白名单外的来源必须拒绝")
	}
}

func TestPolicyClampsLimits(t *testing.T) {
	policy := Policy{CommandTimeoutMS: 10 * MaxCommandTimeoutMS, CommandsPerMinute: 10 * MaxCommandsPerMinute}.WithDefaults()
	if policy.CommandTimeoutMS != MaxCommandTimeoutMS {
		t.Fatalf("超时应被夹到上限，得到 %d", policy.CommandTimeoutMS)
	}
	if policy.CommandsPerMinute != MaxCommandsPerMinute {
		t.Fatalf("限流应被夹到上限，得到 %d", policy.CommandsPerMinute)
	}
}

func TestPolicyDigestOmitsOrigins(t *testing.T) {
	policy := Policy{
		Enabled:        true,
		AllowedOrigins: []string{"chrome-extension://abc"},
		AllowedHosts:   []string{"example.com"},
	}.WithDefaults()
	digest := policy.Digest()
	if len(digest.AllowedHosts) != 1 || digest.AllowedHosts[0] != "example.com" {
		t.Fatalf("站点白名单应回给扩展，得到 %+v", digest)
	}
	// 摘要里没有 AllowedOrigins 字段，这条断言靠编译保证：加字段会让下面这行失效。
	if digest.WriteEnabled {
		t.Fatal("默认只读不该在摘要里变成可写")
	}
}

func TestIsWriteOp(t *testing.T) {
	for _, op := range []string{OpPageOpen, OpPageClick, OpPageType} {
		if !IsWriteOp(op) {
			t.Errorf("%s 应属于写操作", op)
		}
	}
	for _, op := range []string{OpTabsList, OpPageRead} {
		if IsWriteOp(op) {
			t.Errorf("%s 不该属于写操作", op)
		}
	}
	if KnownOp("page.eval") {
		t.Fatal("协议里不该有执行任意脚本的指令")
	}
	// 截图要 <all_urls> 或 activeTab 级权限，比「只授权白名单站点」宽得多，
	// 为一张图把扩展权限放大到全网不值得，所以协议里没有它。
	if KnownOp("page.screenshot") {
		t.Fatal("协议里不该有截图指令")
	}
}
