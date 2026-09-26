// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// browser_handoff：机器人做到一半，碰上只能主人亲手做的一步（登录、扫码、输验证码、
// 过人机验证），请主人在 WebUI 的内置浏览器里接手。
//
// 照 Cloudflare Browser Run 的 handoff 和 OpenAI Operator 的做法：说清楚要人做什么，
// 人做完点「完成」，机器人接着做。区别在于一轮对话最多跑几分钟、每次工具调用最多一
// 分钟，不能让工具一直等着人。所以这个工具只登记请求、把说明当作这一轮的回复发出去，
// 这一轮就结束；主人点了「完成」或「做不了」，再由 resumeAfterBrowserHandoff 在原来
// 的对话里跑一轮，带着原话、待办和结果接着做。
const (
	dianaBrowserHandoffToolName = "browser_handoff"
	// browserHandoffTaskMaxRunes 是待办的长度上限：醒来时只看得到它，要写完整，但不是
	// 用来抄整段对话的。
	browserHandoffTaskMaxRunes = 1500
	// browserHandoffReasonMaxRunes 是「请主人做什么」的长度上限，它要显示在浏览器页上。
	browserHandoffReasonMaxRunes = 60
	defaultBrowserHandoffMinutes = 15
	maxBrowserHandoffMinutes     = 30
)

// 交接的结果，和 model/browserbox 的 Handoff* 常量一一对应。
const (
	browserHandoffDone      = "done"
	browserHandoffFailed    = "failed"
	browserHandoffExpired   = "expired"
	browserHandoffCancelled = "cancelled"
)

// browserHandoffRequester 是内置浏览器可选实现的交接登记，由 model/browserbox.Bot 实现。
type browserHandoffRequester interface {
	RequestHandoff(reason string, timeout time.Duration, done func(outcome string)) (string, error)
}

type dianaBrowserHandoffTool struct {
	runtime *Runtime
	event   MessageEvent
	cfg     BotConfig
}

func newDianaBrowserHandoffTool(r *Runtime, event MessageEvent, cfg BotConfig) *dianaBrowserHandoffTool {
	return &dianaBrowserHandoffTool{runtime: r, event: event, cfg: cfg}
}

func (t *dianaBrowserHandoffTool) Name() string { return dianaBrowserHandoffToolName }

func (t *dianaBrowserHandoffTool) Description() string {
	return `请主人亲手在内置浏览器里做一步：登录、扫码、输短信或邮箱验证码、过人机验证、确认付款这类你做不了、也不该代劳的操作。` +
		`调用后会把说明发给主人（去控制台「浏览器」页点「开始处理」，做完点「完成」），这一轮随即结束；` +
		`主人处理完你会在这个对话里被叫醒接着做，醒来时只看得到 task，所以 task 要写完整。` +
		`先把要处理的页面在内置浏览器里打开再调用。自己能做完的事不要调用；同一步主人说过做不了就别再请。`
}

func (t *dianaBrowserHandoffTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"reason", "task"}, map[string]any{
		"reason":       toolStringParam(fmt.Sprintf("请主人做什么，一个动宾短语，比如「登录小红书」「扫码登录微信读书」「过一下人机验证」。会显示在浏览器页上，不超过 %d 字。", browserHandoffReasonMaxRunes)),
		"task":         toolStringParam("交接之后要接着做完的事，写给之后的你自己看：主人要什么、做到哪一步了、接下来怎么做、最后回复什么。醒来时你只看得到这段话和主人原来那条消息。"),
		"wait_minutes": toolIntParam(fmt.Sprintf("最多等主人多少分钟，默认 %d。", defaultBrowserHandoffMinutes), 1, maxBrowserHandoffMinutes),
	})
}

type browserHandoffResult struct {
	OK      bool   `json:"ok"`
	Notice  string `json:"notice,omitempty"`
	Message string `json:"message"`
}

// browserHandoffJob 是交接有了结果之后接着做要用的东西。只存在内存里：Diana 重启之后
// 等着的交接就没了，主人点「完成」也叫不醒机器人，得重新说一遍。
type browserHandoffJob struct {
	event    MessageEvent
	reason   string
	task     string
	original string
	wait     time.Duration
}

func (t *dianaBrowserHandoffTool) Run(ctx context.Context, input map[string]any) (string, error) {
	reason := strings.Trim(strings.TrimSpace(inputString(input, "reason")), "。.")
	task := strings.TrimSpace(inputString(input, "task"))
	if reason == "" || task == "" {
		return "", fmt.Errorf("reason 和 task 都要写：reason 是请主人做什么，task 是交接之后你要接着做完的事")
	}
	if n := len([]rune(reason)); n > browserHandoffReasonMaxRunes {
		return "", fmt.Errorf("reason 有 %d 字，超过 %d 字；只写请主人做的那一步，别的写进 task", n, browserHandoffReasonMaxRunes)
	}
	if n := len([]rune(task)); n > browserHandoffTaskMaxRunes {
		return "", fmt.Errorf("task 有 %d 字，超过 %d 字；写要点就够了", n, browserHandoffTaskMaxRunes)
	}
	minutes := chatHistoryPositiveInt(input, "wait_minutes", defaultBrowserHandoffMinutes, maxBrowserHandoffMinutes)
	requester, ok := t.runtime.browserBoxFor(t.cfg).(browserHandoffRequester)
	if !ok {
		return "", fmt.Errorf("内置浏览器没有打开，没法请主人在里面操作；可以直接在对话里告诉主人要他做什么")
	}
	job := browserHandoffJob{
		event:    t.event,
		reason:   reason,
		task:     task,
		original: firstNonEmpty(strings.TrimSpace(PlainText(t.event.Segments)), strings.TrimSpace(t.event.RawMessage)),
		wait:     time.Duration(minutes) * time.Minute,
	}
	runtime := t.runtime
	if _, err := requester.RequestHandoff(reason, job.wait, func(outcome string) {
		runtime.resumeAfterBrowserHandoff(job, outcome)
	}); err != nil {
		return "", err
	}
	notice := fmt.Sprintf("%s这一步得你亲手来：打开控制台的「浏览器」页点「开始处理」，弄好了点「完成」，我接着往下做（%d 分钟内有效）",
		reason, minutes)
	body, err := json.Marshal(browserHandoffResult{OK: true, Notice: notice, Message: "已经请主人接手，这一轮到此结束"})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// TerminalResult 让这一轮以交接说明收尾：说明是固定的一段话，保证主人一定看得到去哪、
// 点什么；模型再补一句只会重复。
func (t *dianaBrowserHandoffTool) TerminalResult(output string) (string, bool) {
	var result browserHandoffResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.OK || strings.TrimSpace(result.Notice) == "" {
		return "", false
	}
	return result.Notice, true
}

var _ agent.TerminalResultTool = (*dianaBrowserHandoffTool)(nil)

// 发消息和跑一轮回复，测试里替换掉。
var (
	browserHandoffSend = func(r *Runtime, ctx context.Context, event MessageEvent, text string) error {
		return r.send(ctx, event, text)
	}
	browserHandoffReply = func(r *Runtime, ctx context.Context, cfg BotConfig, event MessageEvent, relationship RelationshipPolicy, messages []llm.Message, tools ...agent.Tool) (string, error) {
		return r.generateReply(ctx, cfg, event, relationship, messages, nil, tools...)
	}
)

// resumeAfterBrowserHandoff 在交接有了结果之后，回到原来的对话接着做。
func (r *Runtime) resumeAfterBrowserHandoff(job browserHandoffJob, outcome string) {
	ctx := context.Background()
	switch outcome {
	case browserHandoffCancelled:
		// 被新的交接顶掉，或者浏览器停了：机器人已经换了做法，不用再叫醒它。
		return
	case browserHandoffExpired:
		// 没人处理不值得再跑一轮模型，说一声就好。
		_ = browserHandoffSend(r, ctx, job.event, fmt.Sprintf("「%s」那一步等了 %d 分钟没人处理，先搁着了，需要的话再叫我",
			job.reason, int(job.wait/time.Minute)))
		return
	case browserHandoffDone, browserHandoffFailed:
	default:
		return
	}
	cfg := r.effectiveConfigForEvent(job.event)
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultBotConfig().WithDefaults().RequestTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	relationship := r.relationshipPolicy(runCtx, job.event)
	// 工具只挂给主人；这里再核一遍，配置在这期间改过也不会让别人借到这一轮。
	if !cfg.AgentEnabled || !relationship.Owner {
		return
	}
	result := fmt.Sprintf("主人已经在内置浏览器里做完了「%s」，浏览器交还给你了。先确认页面确实到了该到的状态（比如已经登录），再接着把待办做完，最后把结果告诉主人。", job.reason)
	if outcome == browserHandoffFailed {
		result = fmt.Sprintf("主人表示「%s」这一步做不了。不要再为同一步请主人接手；能换个办法就换，换不了就简短告诉主人现在卡在哪。", job.reason)
	}
	messages := []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: r.systemPromptWithRelationship(job.event, nil, false, relationship) +
				"\n本次是浏览器交接之后接着做：你之前做到一半，请主人在内置浏览器里亲手做了一步，现在有了结果。" +
				"按待办接着把事情做完，最终只返回要发给主人的内容，保持当前人设和自然语气。",
		},
		{
			Role: llm.RoleUser,
			Content: fmt.Sprintf("【当前需要回复的消息】\n接着做之前的事。当前时间：%s。\n主人原来那条消息：%s\n交接前你记下的待办：%s\n交接结果：%s",
				time.Now().Format("2006-01-02 15:04:05 MST"), firstNonEmpty(job.original, "（没有文字）"), job.task, result),
		},
	}
	reply, err := browserHandoffReply(r, runCtx, cfg, job.event, relationship, messages, newDianaBrowserHandoffTool(r, job.event, cfg))
	if err != nil || strings.TrimSpace(reply) == "" {
		detail := "没有生成结果"
		if err != nil {
			detail = err.Error()
			if runes := []rune(detail); len(runes) > 120 {
				detail = string(runes[:120]) + "…"
			}
		}
		_ = browserHandoffSend(r, ctx, job.event, "浏览器那一步收到了，但接着做的时候出错了："+detail)
		return
	}
	_ = browserHandoffSend(r, ctx, job.event, reply)
}
