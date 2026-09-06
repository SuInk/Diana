package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Captured model output which previously produced twelve separate messages.
const travelDocumentRegression = `周末两天可以走一条“老成都＋市井夜生活＋熊猫”的线，节奏不会太赶喵

**Day 1｜城市经典线**
上午去人民公园，喝盖碗茶、看看鹤鸣茶社，再步行到宽窄巷子逛一圈，巷子商业化比较重，重点看建筑和小吃就好
中午在奎星楼街或魁星楼附近吃川菜、小吃，想吃火锅可以留到晚上
下午去武侯祠和旁边的锦里，武侯祠本身更值得细看，锦里适合傍晚亮灯后顺路逛
晚上去玉林路或望平街一带吃火锅、串串，饭后沿九眼桥到安顺廊桥散步看夜景

**Day 2｜熊猫＋东郊文艺线**
早上尽量早去成都大熊猫繁育研究基地，建议开园就到，熊猫早上更活跃，花花通常人很多，想看她要预留排队时间
中午回市区吃饭，下午选一个方向：喜欢历史去金沙遗址博物馆，喜欢拍照、咖啡和旧厂房就去东郊记忆
傍晚去建设路小吃街收尾，锅盔、冰粉、蛋烘糕、兔头都能试试，但别一次点太多，容易吃撑

几个小提醒：熊猫基地最好提前预约，周一闭馆的博物馆和部分景点要先核对；宽窄巷子、锦里不用安排太久；火锅点微辣也很有劲，不能吃辣就直接说清汤或鸳鸯锅

如果你是第一次来，这条线最稳；要是更想看自然风景，可以把 Day 2 换成青城山＋都江堰的一日往返`

// 文档按小节分条，两个方向都要防住：既不能把一份行程打成十几条（每个时间点一条），
// 也不能把一千字塞进一个气泡。真人发行程是一天一条。
func TestDocumentRepliesSplitAtSections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		{"prose sections", travelDocumentRegression, []string{"周末两天可以走一条", "**Day 1｜城市经典线**", "**Day 2｜熊猫＋东郊文艺线**"}},
		{"plain day labels", "第1天：休息\n上午入住\n下午喝茶\n第2天：市内\n上午参观\n下午返程", []string{"第1天：休息", "第2天：市内"}},
		{"spaced day labels", "第 1 天：休息\n上午入住\n下午喝茶\n第 2 天：市内\n上午参观\n下午返程", []string{"第 1 天：休息", "第 2 天：市内"}},
		{"english day labels", "Day 1: Arrival\nCheck in\nExplore\nDay 2: City\nMuseum\nReturn", []string{"Day 1: Arrival", "Day 2: City"}},
		{"bold labels", "**周六｜老城区**\n上午喝茶\n下午逛街\n**周日｜休息**\n上午参观\n下午返程", []string{"**周六｜老城区**", "**周日｜休息**"}},
		{"underscore labels", "__备份__\n复制文件\n检查数据\n__恢复__\n替换文件\n验证结果", []string{"__备份__", "__恢复__"}},
		{"step labels", "第一步：检查\n确认路径\n查看日志\n第二步：恢复\n还原文件\n重启服务", []string{"第一步：检查", "第二步：恢复"}},
		{"markdown headings", "## 检查配置\n确认路径\n查看日志\n## 重启服务\n确认结果", []string{"## 检查配置", "## 重启服务"}},
		// 清单、表格和代码是一个整体，仍然整块发。
		{"numbered list", "1. 检查配置\n2. 重启服务\n3. 查看日志", []string{"1. 检查配置"}},
		{"table", "| 项目 | 时间 |\n| --- | --- |\n| A | 上午 |\n| B | 下午 |", []string{"| 项目 | 时间 |"}},
		{"list with code", "1. 装依赖\n```sh\nnpm i\n```\n2. 运行", []string{"1. 装依赖"}},
		{"two code blocks", "端口被占了，先查：\n```bash\nlsof -i:8080\n```\n看到 PID 后：\n```bash\nkill 1\n```", []string{"端口被占了，先查："}},
		// 一串条目之后的说明是给整份文档的，不属于最后一个条目。
		{"notes after list", "先看两点\n- 检查配置\n- 重启服务\n这是说明\n这是补充\n还有一句总结", []string{"先看两点", "这是说明"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !isDocumentReply(tc.input) {
				t.Fatalf("document not recognized: %q", tc.input)
			}
			for name, split := range map[string]func(string, chatSplitLimits) []string{"chat": splitChatReply, "forward": splitForwardReply} {
				got := split(tc.input, chatSplitLimits{})
				if len(got) != len(tc.want) {
					t.Fatalf("%s split into %d messages, want %d: %#v", name, len(got), len(tc.want), got)
				}
				for i, prefix := range tc.want {
					if !strings.HasPrefix(got[i], prefix) {
						t.Fatalf("%s message %d = %q, want prefix %q", name, i+1, got[i], prefix)
					}
				}
				// 分条只是断开，不许丢字也不许改写。
				if !reflect.DeepEqual(strings.Fields(strings.Join(got, "\n")), strings.Fields(tc.input)) {
					t.Fatalf("%s changed document content: %#v", name, got)
				}
			}
		})
	}
}

// 关掉自然分条之后文档仍然整条发：那一档只认显式标记。
func TestDocumentRepliesStayWholeWhenNaturalSplitIsOff(t *testing.T) {
	got := splitChatReply(travelDocumentRegression, chatSplitLimits{MarkerOnly: true})
	if len(got) != 1 {
		t.Fatalf("marker-only setting ignored: %d messages", len(got))
	}
}

func TestDocumentGuardPreservesChatAndExplicitMarkers(t *testing.T) {
	chat := "终于修好了\n原来少了个等号\n这下能下班了"
	if isDocumentReply(chat) || len(splitChatReply(chat, chatSplitLimits{})) != 3 {
		t.Fatal("ordinary chat collapsed")
	}
	// 文档本身分成三个小节，显式标记再加一条。
	if got := splitChatReply(travelDocumentRegression+notificationSplitMarker+"补充一句", chatSplitLimits{}); len(got) != 4 || got[3] != "补充一句" {
		t.Fatalf("explicit boundary lost: %q", got)
	}
	if got := splitChatReply(chat, chatSplitLimits{MarkerOnly: true}); len(got) != 1 {
		t.Fatal("marker-only setting ignored")
	}
	if isDocumentReply("明天是 Day 1，别紧张\n后天还有 Day 2 呢") {
		t.Fatal("prose is not a section heading")
	}
}

type documentForwardRejectChannel struct{ recordingChannel }

func (c *documentForwardRejectChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	_, _ = c.recordingChannel.CallAPI(ctx, action, params)
	return nil, errors.New("forward unsupported")
}

func TestDocumentDeliveryDoesNotFloodOnForwardFallback(t *testing.T) {
	for _, mode := range []string{"plain", "forward", "forward_failed"} {
		t.Run(mode, func(t *testing.T) {
			base := &recordingChannel{}
			var channel Channel = base
			if mode == "forward_failed" {
				reject := &documentForwardRejectChannel{}
				base, channel = &reject.recordingChannel, reject
			}
			threshold := 0
			if mode != "plain" {
				threshold = 100
			}
			r := NewRuntime(BotConfig{BotAccount: "42", ForwardReplyThreshold: threshold, SendChunkIntervalMS: 1}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user", SelfID: "42"}
			_, err := r.sendDecorated(context.Background(), event, travelDocumentRegression, outboundDecoration{})
			if err != nil {
				t.Fatal(err)
			}
			sent := base.sentSnapshot()
			if mode == "forward" {
				calls := recordedCallsByAction(base.callsSnapshot(), "send_group_forward_msg")
				if len(calls) != 1 || len(sent) != 0 {
					t.Fatalf("forward calls=%d plain messages=%d", len(calls), len(sent))
				}
			} else {
				// 按小节发，不是一条巨型气泡，也不是每行一条。
				if len(sent) != 3 {
					t.Fatalf("sent %d messages instead of three sections", len(sent))
				}
				var joined []string
				for _, msg := range sent {
					joined = append(joined, msg.Text)
				}
				if !reflect.DeepEqual(strings.Fields(strings.Join(joined, "\n")), strings.Fields(travelDocumentRegression)) {
					t.Fatal("document content lost")
				}
			}
		})
	}
}
