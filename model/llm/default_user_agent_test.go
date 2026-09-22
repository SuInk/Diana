package llm

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var versionLike = regexp.MustCompile(`\d+\.\d+`)

// TestDefaultUserAgentSelfIdentifies 钉住默认 User-Agent 的两条性质。
//
// 一是自报家门而不是冒充别的客户端：以前这里写死 "codex-cli/0.142.0"，而订阅转发
// 网关普遍按 originator 加 User-Agent 双因子认客户端，只伪装 UA 本来就是半套；真要
// 冒充，配置档里填就是了，不该是代码里的默认值。
//
// 二是不含版本号。带版本号的默认值只能是常量，上游按最低版本卡的时候它就过期了，
// 而表现是一个看不出所以然的拒绝。
func TestDefaultUserAgentSelfIdentifies(t *testing.T) {
	ua := DefaultOpenAICompatibleUserAgent
	if !strings.HasPrefix(ua, "diana (") {
		t.Fatalf("默认 UA = %q，应当以产品名自报家门", ua)
	}
	// 冒充成本很低、回报看着很直接，所以这条容易被「顺手」改回去，用测试挡住。
	for _, impostor := range []string{"codex", "claude", "cursor", "gpt"} {
		if strings.Contains(strings.ToLower(ua), impostor) {
			t.Fatalf("默认 UA = %q，不该冒充 %q", ua, impostor)
		}
	}
	// 只拦版本号形态（1.2 这种），不能一概拦数字——架构名本来就带数字（arm64、amd64）。
	if versionLike.MatchString(ua) {
		t.Fatalf("默认 UA = %q，不该带版本号——会随上游的版本门槛过期", ua)
	}
	for _, part := range []string{runtime.GOOS, runtime.GOARCH} {
		if !strings.Contains(ua, part) {
			t.Fatalf("默认 UA = %q，缺少平台信息 %q", ua, part)
		}
	}
}

// TestUserAgentWithDefaultPrefersConfigured 配置档填了就用填的，这是冒充特定客户端
// 的唯一入口，不能被默认值盖掉。
func TestUserAgentWithDefaultPrefersConfigured(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, UserAgent: "codex-cli/0.142.0"}
	if got := cfg.UserAgentWithDefault(); got != "codex-cli/0.142.0" {
		t.Fatalf("UserAgentWithDefault = %q, want 配置档里的值", got)
	}
	bare := ProviderConfig{Provider: ProviderOpenAICompatible}
	if got := bare.UserAgentWithDefault(); got != DefaultOpenAICompatibleUserAgent {
		t.Fatalf("UserAgentWithDefault = %q, want 内置默认", got)
	}
}
