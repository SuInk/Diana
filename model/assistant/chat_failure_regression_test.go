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
