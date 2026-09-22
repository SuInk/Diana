package assistant

import (
	"errors"
	"fmt"
	"github.com/SuInk/diana/model/llm"
	"strings"
	"testing"
)

func TestUnverifiedRejectionUsesFailoverWithoutPolicyClassification(t *testing.T) {
	err := fmt.Errorf("provider failed: %w", llm.ErrUnverifiedRejection)
	if !shouldFailoverLLMError(err) || !shouldFailoverWithoutSameProfileRetry(err) {
		t.Fatal("unverified rejection must try another configured provider")
	}
	if isContentPolicyRejection(err) {
		t.Fatal("unverified rejection incorrectly labeled content policy")
	}
	if shouldFailoverLLMError(errors.New("content_policy_violation")) {
		t.Fatal("confirmed policy behavior changed")
	}
	if !strings.Contains(publicChatErrorMessage(err), "不能据此判断") {
		t.Fatal("public notice overstates the cause")
	}
}

func TestMalformedLayoutMarkersDoNotLeak(t *testing.T) {
	for _, marker := range []string{"[diana-line]", "[ diana-line]", "[diana-line ]", "[\tdiana - line\t]"} {
		if got := normalizeReply("第一行"+marker+"第二行", 0); got != "第一行"+notificationLineMarker+"第二行" {
			t.Fatalf("%q => %q", marker, got)
		}
	}
	code := "`[ diana-line]`\n```text\n[diana-line]\n```"
	if got := normalizeLegacyLayoutMarkers(code); got != code {
		t.Fatalf("code changed: %q", got)
	}
	if got := normalizeLegacyLayoutMarkers("a[ diana-msg ]b"); got != "a"+notificationSplitMarker+"b" {
		t.Fatalf("split marker=%q", got)
	}
}

func TestBrowserSkipsCQImageURLWithTrailingStickerLabel(t *testing.T) {
	imageURL := "https://gxh.vip.qq.com/club/item/parcel/item/b5/test/raw300.gif"
	request := PluginRequest{Text: "[CQ:image,url=" + imageURL + "]&#91;哈气&#93; https://example.com/page", Event: MessageEvent{
		RawMessage: "[CQ:image,url=" + imageURL + "]&#91;哈气&#93;",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURL}}},
	}}
	urls := extractBrowserRenderURLs(request)
	if len(urls) != 1 || urls[0] != "https://example.com/page" {
		t.Fatalf("render URLs=%v", urls)
	}
}

// 把 intent 整组绑到只做判断的模型（TypeSafe Jev）之后，没备判断题表的判定用途
// 会拿到 ErrDecisionRequired。那是能力不匹配，不是上游故障，必须降级到下一档
// 对话模型——否则语义承接、发送前审核、记忆抽取这些会整条失败，线上实测过。
func TestDecisionOnlyProviderFallsOverToNextProfile(t *testing.T) {
	if !shouldFailoverLLMError(llm.ErrDecisionRequired) {
		t.Fatal("判断模型答不了文本时必须降级")
	}
	wrapped := fmt.Errorf("绑定的是只做判断的模型（%w）", llm.ErrDecisionRequired)
	if !shouldFailoverLLMError(wrapped) {
		t.Fatal("包装过的同一个错误也要降级")
	}
}
