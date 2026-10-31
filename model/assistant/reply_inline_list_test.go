package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestInlineReplyListsBoundaries(t *testing.T) {
	for _, input := range []string{
		"从第 0 代升级到第 1 代，性能更好。",
		"第 1 步安装；第 3 步运行",
		"第 1 步安装；第 2 代产品",
		"- 第 0 代全模拟；第 1 代共享内存",
		"> 第 0 代全模拟；第 1 代共享内存",
		"```text\n第 0 代全模拟；第 1 代共享内存\n```",
		"`第 0 代全模拟；第 1 代共享内存`",
		"第 1 步；第 2 步",
		"这页演进线画得挺清楚的，从全模拟一路压到批量免打扰，最后为了快照放弃 vhost-user 也讲得通不过 RL 训练那部分拿单进程 MAP_PRIVATE 保快照，代价就是放弃了 virtiofsd 那条路的吞吐天花板，16 路并行的时候 I/O 会不会成为新瓶颈，这个得拿真实负载量一下才知道够不够",
		"普通长段不应该仅仅因为长度变成列表。",
	} {
		if got := formatInlineReplyLists(input); got != input {
			t.Errorf("changed protected prose: %q => %q", input, got)
		}
	}
	for _, input := range []string{"第 0 代全模拟；第 1 代共享内存", "步骤：第一步安装；第二步启动"} {
		got := formatInlineReplyLists(input)
		if strings.Count(got, "- ") != 2 {
			t.Errorf("missing list: %q", got)
		}
		if again := formatInlineReplyLists(got); again != got {
			t.Errorf("not idempotent: %q", again)
		}
	}
}

func TestInlineReplyListsReportedProductionReply(t *testing.T) {
	raw := "嗯，这四代演进讲得挺清楚的：第 0 代全模拟每次写寄存器都退一次，32 笔就是 200 多次退出；第 1 代 virtqueue 把参数搬运挪进共享内存，只剩敲门那一次；第 2 代 ioeventfd/irqfd/EVENT_IDX 把 32 次敲门合成 1 次，虚拟 CPU 几乎不用停；第 3 代为了快照回滚拒掉 vhost-user 留在单进程 MAP_PRIVATE，这个取舍挺实在的——你要\"随时暂停拍快照派生 16 个分支\"，vhost-user 那个跨进程共享内存的模型天然跟这个冲突，外部进程手里的半截状态也确实没法一起进快照。这个选型逻辑比单纯抄云厂商的成熟，知道自己的核心约束在哪"
	p := &compressionTestProvider{}
	r := compressionTestRuntime(p)
	cfg := BotConfig{MarkdownToPlain: boolPointer(true), ReplyLineSplitEnabled: boolPointer(true), MaxReplyChars: 8000}.WithDefaults()
	event := MessageEvent{Kind: EventKindGroup}
	prepared, err := r.prepareGeneratedReply(context.Background(), cfg, raw, event)
	if err != nil {
		t.Fatal(err)
	}
	parts := splitEventChatReply(prepared, cfg, event)
	if len(parts) != 1 || strings.Count(parts[0], "\n• 第") != 4 {
		t.Fatalf("want one bubble with four items, got %q", parts)
	}
	if strings.ReplaceAll(parts[0], "\n• ", "") != raw {
		t.Fatal("formatting changed original wording")
	}
	if len(p.requests) != 0 || p.generationCalls != 0 {
		t.Fatal("formatting called the model")
	}
}
