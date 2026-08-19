package assistant

import (
	"strings"
	"testing"
)

func TestNormalizeReplyPreservingControlIntentStripsMarkdown(t *testing.T) {
	// QQ 不渲染 Markdown；标志断在这一层会让 cfg.MarkdownToPlain 形同虚设，
	// ** 之类的标记直接漏进聊天窗口（仓库订阅通知就这样漏过）。
	got := normalizeReplyPreservingControlIntent("**群友风格补上投递层（#91）**", 0, true)
	if strings.Contains(got, "**") {
		t.Fatalf("reply still carries Markdown markers: %q", got)
	}
	if got != "群友风格补上投递层（#91）" {
		t.Fatalf("reply = %q", got)
	}
}

func TestNormalizeReplyPreservingControlIntentKeepsMarkdownWhenDisabled(t *testing.T) {
	// 关掉转换时保持原样，不能反过来无条件剥离。
	got := normalizeReplyPreservingControlIntent("**加粗**", 0, false)
	if got != "**加粗**" {
		t.Fatalf("reply = %q, want untouched", got)
	}
	if plain := normalizeReplyPreservingControlIntent("**加粗**", 0); plain != "**加粗**" {
		t.Fatalf("default reply = %q, want untouched", plain)
	}
}

func TestNormalizeReplyPreservingControlIntentKeepsControlMarkers(t *testing.T) {
	// 剥 Markdown 不能把拒答标记一起弄丢。
	got := normalizeReplyPreservingControlIntent("**这条我不回答**"+replyRefusalMarker, 0, true)
	if !strings.HasSuffix(got, replyRefusalMarker) {
		t.Fatalf("refusal marker lost: %q", got)
	}
	if strings.Contains(strings.TrimSuffix(got, replyRefusalMarker), "**") {
		t.Fatalf("Markdown survived alongside the marker: %q", got)
	}
}
