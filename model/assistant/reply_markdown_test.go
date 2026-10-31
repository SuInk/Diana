package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeReplyPreservingControlIntentStripsMarkdown(t *testing.T) {
	// OneBot v11 不渲染 Markdown；标志断在这一层会让 cfg.MarkdownToPlain 形同虚设，
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

// 投递层保留已有列表，并为明确连续编号的行内枚举补换行。
func TestMarkdownReplyDeliveryPreservesModelListBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"markdown_list", "演进如下：\n- **第 0 代**：全模拟\n- **第 1 代**：共享内存", "演进如下：\n• 第 0 代：全模拟\n• 第 1 代：共享内存"},
		{"inline_prose", "演进如下：第 0 代全模拟；第 1 代共享内存", "演进如下：\n• 第 0 代全模拟；\n• 第 1 代共享内存"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compressionTestProvider{}
			r := compressionTestRuntime(p)
			cfg := BotConfig{MarkdownToPlain: boolPointer(true), ReplyLineSplitEnabled: boolPointer(true), MaxReplyChars: 8000}.WithDefaults()
			event := MessageEvent{Kind: EventKindGroup}
			prepared, err := r.prepareGeneratedReply(context.Background(), cfg, tc.raw, event)
			if err != nil {
				t.Fatal(err)
			}
			got := splitEventChatReply(prepared, cfg, event)
			if !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if len(p.requests) != 0 || p.generationCalls != 0 {
				t.Fatal("Markdown conversion must not call the model")
			}
		})
	}
}
