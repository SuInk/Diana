// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 这是这一整个文件存在的理由：围栏以前会被分条按行切开，接收端只看到半个围栏。
func TestSplitChatReplyKeepsCodeFenceWhole(t *testing.T) {
	reply := "看这段：\n```python\nprint(hello)\nreturn 1\n```\n就这样"
	parts := splitChatReply(reply, chatSplitLimits{})

	var fenced string
	for _, part := range parts {
		if strings.Contains(part, "```") {
			if fenced != "" {
				t.Fatalf("围栏被切进了多条消息：%#v", parts)
			}
			fenced = part
		}
	}
	if fenced == "" {
		t.Fatalf("围栏丢了：%#v", parts)
	}
	if strings.Count(fenced, "```") != 2 {
		t.Fatalf("围栏没有成对出现：%q", fenced)
	}
	if !strings.Contains(fenced, "print(hello)") || !strings.Contains(fenced, "return 1") {
		t.Fatalf("代码内容不完整：%q", fenced)
	}
}

// 围栏保住之后还要能被 Telegram 渲染成代码块，否则只是换个地方漏标记。
func TestSplitChatReplyCodeFenceRendersAsPre(t *testing.T) {
	parts := splitChatReply("看这段：\n```go\nname := *ptr\n```\n就这样", chatSplitLimits{})
	for _, part := range parts {
		if !strings.Contains(part, "```") {
			continue
		}
		text, entities := telegramRichText(part, nil)
		if strings.Contains(text, "`") {
			t.Fatalf("反引号漏进正文：%q", text)
		}
		if _, ok := findEntity(entities, "pre"); !ok {
			t.Fatalf("没有渲染成代码块：%#v", entities)
		}
		return
	}
	t.Fatalf("没找到围栏：%#v", parts)
}

// 代码块里的空行是排版的一部分，不能被 collapseBlankLines 抹掉。
func TestSplitChatReplyKeepsBlankLinesInsideFence(t *testing.T) {
	parts := splitChatReply("```go\nfunc a() {}\n\nfunc b() {}\n```", chatSplitLimits{})
	if len(parts) != 1 || !strings.Contains(parts[0], "}\n\nfunc b") {
		t.Fatalf("代码块里的空行被抹掉了：%#v", parts)
	}
}

// 超长代码块必须拆成几个各自完整的围栏，而不是硬切出半个。
func TestSplitFencedBlockSealsEveryPiece(t *testing.T) {
	body := strings.Repeat("line of code here\n", 40)
	pieces := splitFencedBlock("```go\n"+strings.TrimRight(body, "\n")+"\n```", 200)
	if len(pieces) < 2 {
		t.Fatalf("没有拆开：%d 段", len(pieces))
	}
	for index, piece := range pieces {
		if strings.Count(piece, "```") != 2 {
			t.Fatalf("第 %d 段围栏不成对：%q", index, piece)
		}
		if !strings.HasPrefix(piece, "```go") {
			t.Fatalf("第 %d 段没带上语言：%q", index, piece)
		}
	}
}

// 模型被截断时围栏可能没闭合，不能因此把后面的正文全吞进代码块。
func TestMaskFencedCodeBlocksClosesUnterminatedFence(t *testing.T) {
	masked, blocks := maskFencedCodeBlocks("说明\n```go\nfunc a() {}")
	if len(blocks) != 1 {
		t.Fatalf("围栏数 = %d", len(blocks))
	}
	if strings.Count(blocks[0], "```") != 2 {
		t.Fatalf("没有补上收尾：%q", blocks[0])
	}
	if strings.Contains(masked, "```") {
		t.Fatalf("占位没换干净：%q", masked)
	}
}

// 没有围栏的普通消息必须原样通过，避免给绝大多数消息引入无谓的差异。
func TestMaskFencedCodeBlocksLeavesPlainTextAlone(t *testing.T) {
	source := "今天天气不错\n出去走走吧"
	masked, blocks := maskFencedCodeBlocks(source)
	if masked != source || blocks != nil {
		t.Fatalf("纯文本被改动了：%q %#v", masked, blocks)
	}
}

// 纯文本平台在 normalizeReply 阶段就把围栏删掉了，这一层应当什么都不做。
func TestSplitChatReplyUnaffectedAfterMarkdownDowngrade(t *testing.T) {
	plain := markdownToPlain("看这段：\n```python\nprint(hello)\n```\n就这样")
	if strings.Contains(plain, "```") {
		t.Fatalf("降级没删掉围栏：%q", plain)
	}
	if parts := splitChatReply(plain, chatSplitLimits{}); len(parts) == 0 {
		t.Fatal("降级后的文本分条为空")
	}
}
