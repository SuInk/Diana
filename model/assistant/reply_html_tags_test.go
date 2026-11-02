// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestHTMLInlineTagsToMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name, in, rich, plain string
	}{
		// 生产 OneBot 出站原句。
		{"break", "我这边就按这个来做<br>扩展你随便加，钩子和拦截我来补", "我这边就按这个来做\n扩展你随便加，钩子和拦截我来补", "我这边就按这个来做\n扩展你随便加，钩子和拦截我来补"},
		// gpt-6.1-terra 回放在 QQ 官方机器人上的输出。
		{"strike", "算不出我实际要花多少<s>钱</s>。", "算不出我实际要花多少~~钱~~。", "算不出我实际要花多少钱。"},
		{"bold", "<B>结论</B>：可以 <em>先</em>试", "**结论**：可以 *先*试", "结论：可以 先试"},
		{"code kept", "用 `<b>粗</b>` 写粗体\n```html\n<s>x</s><br>\n```", "用 `<b>粗</b>` 写粗体\n```html\n<s>x</s><br>\n```", "用 `<b>粗</b>` 写粗体\n```html\n<s>x</s><br>\n```"},
		{"unmatched", "<b>开头</i> 和 <div>块</div> 和 <diana-br/>", "<b>开头</i> 和 <div>块</div> 和 <diana-br/>", "<b>开头</i> 和 <div>块</div> 和 <diana-br/>"},
		{"attributes", `<b class="x">带属性</b>`, `<b class="x">带属性</b>`, `<b class="x">带属性</b>`},
	} {
		if got := htmlInlineTagsToMarkdown(tc.in, false); got != tc.rich {
			t.Errorf("%s rich = %q, want %q", tc.name, got, tc.rich)
		}
		if got := htmlInlineTagsToMarkdown(tc.in, true); got != tc.plain {
			t.Errorf("%s plain = %q, want %q", tc.name, got, tc.plain)
		}
	}
}

// 走完整的回复规范化：纯文本平台不留 ~~ 或标签，富文本平台留给适配器渲染。
func TestNormalizeReplyConvertsHTMLTags(t *testing.T) {
	if got := normalizeReply("要花多少<s>钱</s>", 0, true); got != "要花多少钱" {
		t.Fatalf("plain = %q", got)
	}
	if got := normalizeReply("要花多少<s>钱</s>", 0, false); got != "要花多少~~钱~~" {
		t.Fatalf("rich = %q", got)
	}
}
