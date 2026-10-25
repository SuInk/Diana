package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestMaiBotReplyLinesKeepsListLines(t *testing.T) {
	rendered := "最近主要是这几块：\r\n\r\n1. 技术折腾\n2. 斗图\n  3. 考学  \n"
	want := "最近主要是这几块：" + notificationLineMarker + "1. 技术折腾" + notificationLineMarker + "2. 斗图" + notificationLineMarker + "3. 考学"
	cfg := DefaultBotConfig()
	if got := maiBotReplyLines(rendered, cfg, MessageEvent{}, replyControlIntent{}, false); got != want {
		t.Fatalf("default config = %q, want %q", got, want)
	}

	compact := false
	cfg.ReplyPreserveLineBreaks = &compact
	if got := maiBotReplyLines(rendered, cfg, MessageEvent{}, replyControlIntent{}, false); got != rendered {
		t.Fatalf("compact config changed rendered text: %q", got)
	}
	if got := maiBotReplyLines(rendered, cfg, MessageEvent{}, replyControlIntent{LineBreakMode: replyLinesPreserve}, false); got != want {
		t.Fatalf("turn preserve over compact config = %q", got)
	}
	if got := maiBotReplyLines(rendered, cfg, MessageEvent{replyLineBreakMode: replyLinesPreserve}, replyControlIntent{}, false); got != want {
		t.Fatalf("event preserve over compact config = %q", got)
	}

	cfg.ReplyPreserveLineBreaks = nil
	if got := maiBotReplyLines(rendered, cfg, MessageEvent{}, replyControlIntent{LineBreakMode: replyLinesCompact}, false); got != rendered {
		t.Fatalf("turn compact changed rendered text: %q", got)
	}
}

// 改写稿转成 [diana-line] 后，发送前的版式整理不能再把列表压成一行。
func TestMaiBotReplyLinesSurvivePrepare(t *testing.T) {
	cfg := DefaultBotConfig()
	r := &Runtime{}
	raw := "这几块：\n1. 技术\n2. 斗图"
	flattened, err := r.prepareGeneratedReply(context.Background(), cfg, raw)
	if err != nil {
		t.Fatalf("prepareGeneratedReply(raw) error = %v", err)
	}
	if strings.Contains(flattened, notificationLineMarker) || strings.Contains(flattened, "\n") {
		t.Fatalf("raw newlines are expected to be flattened, got %q", flattened)
	}
	got, err := r.prepareGeneratedReply(context.Background(), cfg, maiBotReplyLines(raw, cfg, MessageEvent{}, replyControlIntent{}, false))
	if err != nil {
		t.Fatalf("prepareGeneratedReply() error = %v", err)
	}
	if strings.Count(got, notificationLineMarker) != 2 {
		t.Fatalf("prepared reply lost line markers: %q", got)
	}
}

// 代码块里的缩进和空行是代码的一部分，转换时原样保留，发出去之后缩进还在。
func TestMaiBotReplyLinesKeepsCodeBlockVerbatim(t *testing.T) {
	cfg := DefaultBotConfig()
	raw := "示例：\n  ```python\ndef f():\n    return 1\n\nprint(f())  \n```\n就这样"
	want := strings.Join([]string{"示例：", "```python", "def f():", "    return 1", "", "print(f())", "```", "就这样"}, notificationLineMarker)
	got := maiBotReplyLines(raw, cfg, MessageEvent{}, replyControlIntent{}, false)
	if got != want {
		t.Fatalf("maiBotReplyLines() = %q, want %q", got, want)
	}

	prepared, err := (&Runtime{}).prepareGeneratedReply(context.Background(), cfg, got)
	if err != nil {
		t.Fatalf("prepareGeneratedReply() error = %v", err)
	}
	sent := strings.Join(splitChatReply(prepared, chatSplitLimits{}), "\n")
	if !strings.Contains(sent, "def f():\n    return 1") {
		t.Fatalf("code indentation lost after delivery prep: %q", sent)
	}
}
