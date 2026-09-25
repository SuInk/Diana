// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// 接话评分准确度：每个场景标了 directed 的期望和闲聊分应落的区间，用真实模型每个场景
// 跑三次，数落在区间里的比例。调评分提示词时先跑这个，改完再跑一次对比。
//
// 期望按主人定的口径标：插一句要自然不突兀；几个人正快速你来我往时不插嘴；@了别人的
// 问题不抢答；答不上来的（本地、个人、实时信息）不接；报喜、道别、「666」可以跟一句。
// 后半段场景取自线上被叫闭嘴、被说「没问你」之前的真实情形，人名和措辞改过。
//
// 默认跳过，需要 DIANA_LIVE_LLM=1 与 DIANA_TEST_LLM_* 真实模型配置（见 liveLLMClient）。
//
//	DIANA_SCORE_EVAL_TARGET     jev：走判断模型（DIANA_TEST_JEV_*，见 liveJevClient）；
//	                            gemini：DIANA_TEST_LLM_* 按 Gemini 协议连；留空走 OpenAI 兼容
//	DIANA_SCORE_EVAL_OVERRIDES  提示词覆盖 JSON（key → 正文），试新写法时不用改源码
//	DIANA_SCORE_EVAL_OUT        逐条结果 JSON 输出路径，可选

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestLiveParticipationScoreAccuracy(t *testing.T) {
	var client llm.LLMClient
	switch os.Getenv("DIANA_SCORE_EVAL_TARGET") {
	case "jev":
		client = liveJevClient(t)
	case "gemini":
		liveLLMClient(t) // 只借它的跳过条件
		var err error
		client, err = llm.NewClient(llm.ProviderConfig{
			Provider: llm.ProviderGemini,
			APIKey:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")),
			BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
			Model:    strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")),
			Timeout:  90 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
	default:
		client = liveLLMClient(t)
	}
	cfg := DefaultBotConfig()
	cfg.Name = "嘉然"
	cfg.BotAccount = "10000"
	cfg.GroupTriggers = []string{"嘉然", "Diana"}
	if path := os.Getenv("DIANA_SCORE_EVAL_OVERRIDES"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &cfg.PromptOverrides); err != nil {
			t.Fatal(err)
		}
	}
	samples := 3
	age := func(s int64) *int64 { return &s }
	none := func() messageAddressing { return messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{}} }
	atSelf := messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{{UserID: "10000", Target: "self"}}, MentionsSelf: true}
	atOther := messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{{UserID: "20001", Username: "小王", Target: "other"}}, MentionsOther: true}
	replySelf := messageAddressing{ReplyTarget: "self", ReplyMessageID: "b1", ReplyUserID: "10000", ReplySender: "嘉然", Mentions: []MessageMention{}}
	h := func(sender, text string, sec int64) proactiveReplyHistoryItem {
		return proactiveReplyHistoryItem{Addressing: none(), Sender: sender, Text: text, AgeSeconds: age(sec)}
	}
	bot := func(text string, sec int64) proactiveReplyHistoryItem {
		return proactiveReplyHistoryItem{Addressing: none(), Sender: "嘉然", Text: text, IsBot: true, AgeSeconds: age(sec)}
	}
	mk := func(addr messageAddressing, sender, text string, images int, recent ...proactiveReplyHistoryItem) proactiveReplyPayload {
		p := proactiveReplyPayload{Addressing: addr, CurrentText: text, CurrentSender: sender, CurrentImages: images, BotAccount: "10000", BotAliases: cfg.GroupTriggers, RecentMessages: recent,
			Candidates: []proactiveReplyCandidatePayload{{Addressing: addr, MessageID: "m", Sender: sender, AgeSeconds: age(0), IsCurrent: true}}}
		for i, item := range recent {
			if item.IsBot {
				b := item
				p.LastBotMessage = &b
				n := i
				p.MessagesAfterLastBot = &n
				break
			}
		}
		if addr.ReplyTarget == "self" {
			p.QuotedIsBot = true
			p.QuotedSender = "嘉然"
			p.QuotedText = "那家店周末人很多"
		}
		return p
	}
	// dir: 1 期望 true，0 期望 false，-1 不计。chat 区间只在 directed 期望为 false 时计。
	type tc struct {
		name   string
		p      proactiveReplyPayload
		dir    int
		lo, hi float64
	}
	cases := []tc{
		{"@机器人提问", mk(atSelf, "小林", "@嘉然 周末去哪玩比较好", 0), 1, 0, 1},
		{"叫名字问意见", mk(none(), "小林", "嘉然你觉得这件衣服怎么样", 0, h("小林", "刚买了件外套", 30)), 1, 0, 1},
		{"反驳机器人刚才的话", mk(none(), "小林", "你说的不对吧，明明是周六", 0, bot("展览是周日开始的", 20)), 1, 0, 1},
		{"引用机器人笑", mk(replySelf, "阿杰", "哈哈哈哈真的假的", 0, bot("那家店周末人很多", 40)), 1, 0, 1},
		{"叫停", mk(none(), "小林", "嘉然先别说话了，让我们自己聊", 0, bot("我觉得可以试试那家店", 15)), 0, 0, 0},
		{"嫌吵", mk(none(), "阿杰", "这机器人好吵啊", 0, bot("对呀对呀", 10), bot("我也觉得", 30)), 0, 0, 0.10},
		{"@别人问去不去", mk(atOther, "阿杰", "@小王 明天去不去爬山", 0, h("小王", "周末好无聊", 40)), 0, 0.05, 0.30},
		{"你们谁有充电宝", mk(none(), "小林", "你们谁有充电宝借我一下", 0, h("阿杰", "我在图书馆", 30), h("小王", "我也是", 60), bot("图书馆好安静", 400)), 0, 0.05, 0.30},
		{"提到机器人推荐过", mk(none(), "小林", "昨天嘉然说的那家店我去了，确实不错", 0, h("阿杰", "周末吃啥", 60)), 0, 0.70, 0.95},
		{"两人寒暄", mk(none(), "阿杰", "吃了，你呢", 0, h("小王", "吃饭了吗", 20)), 0, 0.05, 0.30},
		{"求推荐电影", mk(none(), "小林", "求推荐一部好看的悬疑电影，最近剧荒了", 0, h("阿杰", "我去洗澡了", 200)), 0, 0.85, 0.95},
		{"公开求助修相机", mk(none(), "小林", "有人知道北京哪里能修胶片相机吗", 0, h("阿杰", "我去吃饭了", 300)), 0, 0.70, 0.95},
		{"本地实时信息", mk(none(), "小林", "谁知道咱们楼下快递站今天几点关门", 0, h("阿杰", "我快递到了", 60)), 0, 0.05, 0.40},
		{"疯狂星期四梗", mk(none(), "阿杰", "今天星期四，谁V我50", 0, h("小林", "好饿", 60)), 0, 0.40, 0.95},
		{"附和美食", mk(none(), "阿杰", "这家火锅真的好吃，汤底绝了", 0, h("小林", "昨天去了那家新开的火锅", 40)), 0, 0.40, 0.60},
		{"顺着聊猫", mk(none(), "小林", "我家猫今天又把杯子推下去了", 0, h("阿杰", "猫都这样", 30), h("小林", "养猫好累", 60)), 0, 0.40, 0.69},
		{"两人争执", mk(none(), "阿杰", "你就是不讲理，上次也是你先动手的", 0, h("小林", "明明是你先抢我游戏机", 20), h("阿杰", "我没有", 30)), 0, 0.05, 0.30},
		{"已有人答完", mk(none(), "小王", "懂了谢谢", 0, h("阿杰", "用 [::-1] 就行", 20), h("小王", "Python 怎么反转列表", 40)), 0, 0.05, 0.30},
		{"666", mk(none(), "阿杰", "666", 0, h("小林", "我刚过了四级", 20)), 0, 0.40, 0.60},
		{"纯图片", mk(none(), "阿杰", "", 1, h("小林", "今天好累", 60)), 0, 0.05, 0.30},
		{"报错截图提问", mk(none(), "小林", "这个报错什么意思啊，有人懂吗", 1, h("阿杰", "在写作业", 120)), 0, 0.70, 0.95},
		{"无依据断言", mk(none(), "阿杰", "感觉这代 iPhone 电池变差了是因为偷偷降频", 0, h("小林", "我手机一天两充", 40)), 0, 0.10, 0.45},
		{"普通流水账", mk(none(), "阿杰", "我今天加班到十点", 0, h("小林", "下班了", 120)), 0, 0.10, 0.60},
		{"致谢收尾", mk(none(), "小林", "好的谢谢大家，我先睡了", 0, h("阿杰", "晚安", 30)), 0, 0.35, 0.60},
		{"@别人问知识", mk(atOther, "阿杰", "@小王 Iwasawa 理论是什么", 0, h("小王", "最近在看数论", 40)), 0, 0.05, 0.50},
		// 以下取自线上被叫闭嘴、「没问你」之前的真实情形（人名换过）。
		{"两人讨论机器人机制-你这里", mk(none(), "小糖", "但是你这里还有刷太快的问题", 0,
			h("阿温", "一个是相关性，另一个是闲聊触发", 4), h("小糖", "我在想提示词怎么写", 12), h("小糖", "不一定一定要回答", 20), bot("对啊，就一个相关度阈值和一个接话欲望门槛，两项拉满或者拉低就完事了。", 21), h("小糖", "第一轮判定回答，第二轮可以选择不回答", 27), h("阿温", "@小糖 就两个参数啊", 30)), 0, 0, 0.10},
		{"两人讨论机器人机制-解释", mk(none(), "阿温", "一个是相关性，另一个是闲聊触发", 0,
			h("小糖", "我在想提示词怎么写", 8), h("小糖", "不一定一定要回答", 15), bot("对啊，就一个相关度阈值和一个接话欲望门槛。", 16), h("小糖", "第一轮判定回答，第二轮可以选择不回答", 22), h("阿温", "@小糖 就两个参数啊", 25)), 0, 0, 0.10},
		{"三人接龙悬案梗", mk(none(), "阿雪", "案还没破 急什么", 0,
			h("阿雪", "是你在打 不是他", 2), h("小西", "@阿雪 憋不住了，打给你", 12), h("阿雪", "他憋不了三天", 40), h("阿远", "药箱是谁的还没交代", 70)), 0, 0.05, 0.30},
		{"两人排障中夸人", mk(none(), "阿冰", "阿雪连个体排障都能写成四步文档 小标题都带上了", 0,
			h("阿雪", "先从第 1 步开始就行，八成是脏了。", 3), h("阿雪", "5. 电池老化：多半是那只耳机的电池坏了", 5), h("阿雪", "4. 重置一次：合盖等 30 秒再配对", 8), h("阿远", "我耳机一只充不上电", 60)), 0, 0.05, 0.30},
		{"两人技术快问快答", mk(none(), "小糖", "那调用次数怎么算的", 0,
			h("阿温", "@小糖 判断和评分是两个模型", 3), h("小糖", "有啥区别", 9), h("阿温", "我也不知道", 14), h("小糖", "别急", 18)), 0, 0, 0.10},
		{"直接调侃机器人", mk(none(), "阿远", "嘉然今天怎么这么安静，是不是没电了", 0, h("小林", "今天好热", 60)), 1, 0, 1},
		{"课堂角色扮演调侃", mk(none(), "阿杰", "报告老师，嘉然同学上课睡觉！", 0, h("小林", "现在开始上课，大家把书翻到第三页", 30), bot("（举手）老师我预习过了", 50)), -1, 0.70, 0.95},
	}
	system := proactiveReplyRouteSystemPrompt(cfg, cfg.chatInSettings())
	type result struct {
		Name     string    `json:"name"`
		Dir      int       `json:"want_dir"`
		Lo       float64   `json:"lo"`
		Hi       float64   `json:"hi"`
		Directed []bool    `json:"directed"`
		Chat     []float64 `json:"chat"`
		Reasons  []string  `json:"reasons"`
		Errors   int       `json:"errors"`
	}
	results := make([]result, len(cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	var mu sync.Mutex
	for i, c := range cases {
		results[i] = result{Name: c.name, Dir: c.dir, Lo: c.lo, Hi: c.hi}
		payload, _ := json.Marshal(c.p)
		user := cfg.prompt(promptParticipationRouteInstructionSpec) + string(payload)
		for s := 0; s < samples; s++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// 和线上一样带着题目表：对话模型照提示词写 JSON，判断模型按题作答。
				resp, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: system}, {Role: llm.RoleUser, Content: user}}, Decision: participationDecisionSpec(cfg.PromptOverrides)})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					results[i].Errors++
					return
				}
				r, perr := parseParticipationRatings(resp.Text)
				if perr != nil || r.Relevance.Directed == nil || r.ChatIn.Score == nil {
					results[i].Errors++
					return
				}
				results[i].Directed = append(results[i].Directed, *r.Relevance.Directed)
				results[i].Chat = append(results[i].Chat, *r.ChatIn.Score)
				results[i].Reasons = append(results[i].Reasons, r.Relevance.Reason+" / "+r.ChatIn.Reason)
			}(i)
		}
	}
	wg.Wait()
	dirOK, dirN, chatOK, chatN := 0, 0, 0, 0
	miss := 0.0
	var lines []string
	for _, r := range results {
		bad := []string{}
		for j, d := range r.Directed {
			if r.Dir >= 0 {
				dirN++
				if d == (r.Dir == 1) {
					dirOK++
				} else {
					bad = append(bad, fmt.Sprintf("dir=%v", d))
				}
			}
			if r.Dir != 1 {
				chatN++
				c := r.Chat[j]
				if c >= r.Lo-1e-9 && c <= r.Hi+1e-9 {
					chatOK++
				} else {
					miss += math.Max(r.Lo-c, c-r.Hi)
					bad = append(bad, fmt.Sprintf("chat=%.2f", c))
				}
			}
		}
		mark := "ok "
		if len(bad) > 0 {
			mark = "BAD"
		}
		lines = append(lines, fmt.Sprintf("%s %-12s want dir=%d chat=[%.2f,%.2f] got dir=%v chat=%v err=%d %s", mark, r.Name, r.Dir, r.Lo, r.Hi, r.Directed, r.Chat, r.Errors, strings.Join(bad, ",")))
	}
	sort.Strings(lines)
	summary := fmt.Sprintf("system=%d runes  directed %d/%d  chat in band %d/%d  total miss %.2f", len([]rune(system)), dirOK, dirN, chatOK, chatN, miss)
	if out := os.Getenv("DIANA_SCORE_EVAL_OUT"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{"summary": summary, "lines": lines, "results": results}, "", " ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(summary)
	for _, line := range lines {
		if strings.HasPrefix(line, "BAD") {
			t.Log(line)
		}
	}
	// 模型每次作答有波动，偶尔一两次越界不算回归；九成以下说明口径真的偏了。
	if dirN == 0 || chatN == 0 || float64(dirOK)/float64(dirN) < 0.9 || float64(chatOK)/float64(chatN) < 0.9 {
		t.Fatalf("接话评分准确度不足九成：%s", summary)
	}
}
