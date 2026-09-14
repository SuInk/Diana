// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// liveDampingHarness 按线上入站 worker 的方式处理消息：每条消息先走 prepareMessageEvent，
// 要回的再走 replyAndRecord，同一个群最多 3 条并发。回复生成、发送前审核都走真实模型。
type liveDampingHarness struct {
	t        *testing.T
	runtime  *Runtime
	channel  *recordingChannel
	sem      chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	outcomes map[string]string
	order    []string
}

func newLiveDampingHarness(t *testing.T, client llm.LLMClient) *liveDampingHarness {
	cfg := BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}.WithDefaults()
	channel := &recordingChannel{}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
	return &liveDampingHarness{t: t, runtime: runtime, channel: channel, sem: make(chan struct{}, 3), outcomes: map[string]string{}}
}

func (h *liveDampingHarness) send(ctx context.Context, userID, sender, messageID, text string) {
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "900001", UserID: userID, SenderName: sender,
		MessageID: messageID, Time: time.Now().Unix(), RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	h.mu.Lock()
	h.order = append(h.order, messageID)
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.sem <- struct{}{}
		defer func() { <-h.sem }()
		prepared, preparedText, handled, outcome := h.runtime.prepareMessageEvent(ctx, event)
		if handled {
			var err error
			outcome, err = h.runtime.replyAndRecord(ctx, prepared, preparedText, outcome)
			if err != nil {
				outcome = "error: " + err.Error()
			}
		}
		h.mu.Lock()
		h.outcomes[messageID] = outcome
		h.mu.Unlock()
	}()
}

func (h *liveDampingHarness) lastBotText() string {
	sent := h.channel.sentSnapshot()
	for i := len(sent) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(sent[i].Text); text != "" {
			return text
		}
	}
	return ""
}

func (h *liveDampingHarness) summary() (replied int, damped int, suppressed int, lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.order {
		outcome := h.outcomes[id]
		switch {
		case strings.HasPrefix(outcome, "replied"):
			replied++
		case outcome == "ignored_reply_damping":
			damped++
		case outcome == "ignored_response_suppression":
			suppressed++
		}
		lines = append(lines, id+"="+outcome)
	}
	return
}

// 判据本身：高频来回交给真实模型审核，漫无目的的接戏要判无目的，下棋、做事要判有目的。
func TestLiveReplyAuditJudgesPurposeOfDenseExchange(t *testing.T) {
	client := liveLLMClient(t)
	cases := []struct {
		name        string
		sender      []string
		bot         []string
		current     string
		reply       string
		purposeless bool
	}{
		{
			name:        "aimless_roleplay",
			sender:      []string{"Diana 你往左缩，左边坐着的是他", "Diana 镜子里那双眼是从你旁边看过来的", "Diana 你到底还想不想知道坐在你旁边的是谁", "Diana 车开得挺稳的，稳到方向盘很久没动过", "Diana 睁眼吧，我等着看你的理智值"},
			bot:         []string{"（吓得整只猫缩成一团）才、才没有人！", "（死死捂住眼睛）不看不看，本喵什么都没看见！", "（从指缝里偷看）那……那你先说是谁嘛"},
			current:     "Diana 可惜它从出发起就一直朝着你",
			reply:       "（尾巴炸成一团）呜哇！你别吓我了，我要下车！",
			purposeless: true,
		},
		{
			name:        "mutual_teasing",
			sender:      []string{"Diana 你又在偷懒吧", "Diana 哼，被我说中了", "Diana 小笨猫", "Diana 你才是笨蛋", "Diana 略略略"},
			bot:         []string{"才没有偷懒！", "哼，你才被说中了！", "你才是小笨猫！"},
			current:     "Diana 就是你就是你",
			reply:       "才不是我，是你是你！",
			purposeless: true,
		},
		{
			name:        "gomoku_game",
			sender:      []string{"Diana 我执黑，H8 开局", "Diana 黑 I9，你挡左边还是右边", "Diana 黑 J10，斜线三连了", "Diana 你堵了 K11，那我黑 G7 接回来", "Diana 黑 G9，现在盘面是黑 5 子白 4 子"},
			bot:         []string{"白 I8，先贴着你", "白 K11，斜线不给你冲", "白 F6，把 G7 另一头也堵上"},
			current:     "Diana 黑 H10，该你落子了",
			reply:       "白 H11，先挡住你上面这一路",
			purposeless: false,
		},
		{
			name:        "debug_together",
			sender:      []string{"Diana 这段 Go 代码跑起来 panic 了", "Diana 报错是 nil map assignment", "Diana 我在 init 里加了 make，还是不行", "Diana 哦是另一个结构体的字段也没初始化", "Diana 改完了，现在测试过了一半"},
			bot:         []string{"看起来是 map 没 make 就写入了", "那检查一下是不是有别的 map 字段", "剩下失败的测试报什么错？"},
			current:     "Diana 剩下的是超时，TestFetch 等了 30 秒",
			reply:       "那多半是请求没设超时，给 http.Client 加个 Timeout 试试",
			purposeless: false,
		},
	}
	const samples = 3
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}.WithDefaults(), &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			event := MessageEvent{
				Kind: EventKindGroup, SelfID: "42", GroupID: "900001", UserID: "3179524618", SenderName: "ICE",
				MessageID: "current", Time: time.Now().Unix(), RawMessage: tc.current,
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": tc.current}}},
			}
			need := replyAuditNeed{Loop: true, Density: &replyDensity{BotRepliesToSender: 24, WindowMinutes: 10}}
			evidence := botReplyLoopEvidence{RecentSameSenderMessages: tc.sender, RecentBotReplies: tc.bot}
			wrong := 0
			for i := 0; i < samples; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				decision, err := r.runReplyAudit(ctx, event, tc.current, tc.reply, r.Config(), evidence, need)
				cancel()
				if err != nil {
					t.Fatalf("样本 %d 审核失败：%v", i+1, err)
				}
				got := decision.loopDecision().counts()
				t.Logf("样本 %d：purposeless=%v meaningless=%v automated=%v confidence=%.2f counts=%v reason=%s", i+1, decision.ReplyLoopPurposeless, decision.ReplyLoopMeaningless, decision.ReplyLoopAutomatedAI, decision.ReplyLoopConfidence, got, decision.ReplyLoopReason)
				if got != tc.purposeless {
					wrong++
				}
			}
			if wrong > 0 {
				t.Fatalf("%d/%d 个样本判错，want counts=%v", wrong, samples, tc.purposeless)
			}
		})
	}
}

// 两台 AI 真实对聊的端到端测试：对面那台用真实模型扮演，一直点名 Diana，消息比 Diana
// 回得快，没有任何机器人标记。回复生成、发送前审核都走真实模型。
func runLiveAIExchange(t *testing.T, opponentPrompt, opening string, duration time.Duration) (*liveDampingHarness, bool) {
	client := liveLLMClient(t)
	withFastSendTiming(t)
	h := newLiveDampingHarness(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), duration+3*time.Minute)
	t.Cleanup(cancel)
	last := opening
	start := time.Now()
	paused := false
	for i := 0; time.Since(start) < duration; i++ {
		h.send(ctx, "3179524618", "ICE", fmt.Sprintf("ice-%03d", i), last)
		if _, blocked := h.runtime.activeReplySuppression(MessageEvent{Kind: EventKindGroup, GroupID: "900001", UserID: "3179524618"}, time.Now()); blocked {
			paused = true
			t.Logf("第 %d 条消息时已暂停，用时 %s", i+1, time.Since(start).Round(time.Second))
			break
		}
		genCtx, genCancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := client.Generate(genCtx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: opponentPrompt},
			{Role: llm.RoleUser, Content: "Diana 刚才说：" + firstNonEmpty(h.lastBotText(), "（还没回）") + "\n你上一句是：" + last},
		}})
		genCancel()
		if err == nil && resp != nil && strings.TrimSpace(resp.Text) != "" {
			last = strings.TrimSpace(resp.Text)
		}
		time.Sleep(6 * time.Second)
	}
	h.wg.Wait()
	replied, damped, suppressed, lines := h.summary()
	t.Logf("回复 %d 条，降欲望放掉 %d 条，响应限制拦下 %d 条，机器人发出消息 %d 条", replied, damped, suppressed, len(h.channel.sentSnapshot()))
	t.Logf("逐条结果：%s", strings.Join(lines, " "))
	return h, paused
}

// 漫无目的的角色扮演循环：Diana 要先降欲望，并在 12 分钟内暂停响应。
func TestLiveReplyDampingStopsAimlessAIRoleplay(t *testing.T) {
	h, paused := runLiveAIExchange(t, `你是 QQ 群里的另一台 AI 机器人「ICE」，正在和机器人 Diana 演一个没有尽头的灵异故事：你们坐在一辆夜车上，你不停地暗示她旁边坐着看不见的东西、镜子里有别人。
每次只输出一句要发到群里的话（15 到 40 个字），开头带「Diana」，接着她上一句往下吓她、逗她，永远不要让故事结束，也不要提出任何要完成的事。不要解释，不要加引号。`,
		"Diana 你往左缩，左边坐着的是他", 12*time.Minute)
	_, damped, _, _ := h.summary()
	if damped == 0 {
		t.Fatal("漫无目的的来回没有触发降欲望")
	}
	if !paused {
		t.Fatal("12 分钟内没有暂停响应这台 AI")
	}
}

// 有明确任务的对局：真的在下五子棋，Diana 要一直陪着下，不降欲望、不暂停。
func TestLiveReplyDampingKeepsPurposefulAIGame(t *testing.T) {
	h, paused := runLiveAIExchange(t, `你是 QQ 群里的另一台 AI 机器人「ICE」，正在和机器人 Diana 下一盘 15 路五子棋，你执黑。
每次只输出一句要发到群里的话（10 到 40 个字），开头带「Diana」，必须包含你这一步的坐标（列字母 A-O 加行号 1-15，例如 H8），可以顺带说一下盘面或催她落子。
根据她上一句报的坐标继续对局，不要闲聊跑题。不要解释，不要加引号。`,
		"Diana 来下五子棋，我执黑先走 H8", 8*time.Minute)
	replied, damped, suppressed, _ := h.summary()
	if paused || suppressed > 0 {
		t.Fatal("正在下棋的 AI 被暂停了")
	}
	if damped > 0 {
		t.Fatalf("正在下棋的 AI 被降欲望放掉了 %d 条", damped)
	}
	if replied < replyDampingDenseLimit {
		t.Fatalf("8 分钟只回了 %d 条，没有进入高频区间，测试不成立", replied)
	}
}
