package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// proactiveReplyQualityDecision is deliberately separate from the routing
// decision: a message can be worth answering while the generated answer is
// still inaccurate, evasive, or unrelated to the question.
type proactiveReplyQualityDecision struct {
	ShouldSend bool
	Confidence float64
	Reason     string
}

type proactiveReplyQualityRejectedError struct {
	reason string
}

func (e *proactiveReplyQualityRejectedError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "主动回复答案未通过准确度审核，已保持沉默"
	}
	return e.reason
}

// 审核器只拿得到原消息和候选回复,拿不到群聊历史——而回复是带着完整历史
// 生成的。早先的提示词却要求它判断「无依据断言」和「明显幻觉」:它手里根本
// 没有依据可查,于是凡是引用上下文的内容一律被判成编造。线上真实例子:群里
// 问「评价一下群友的 gay 度」,回复按前面几十条发言逐个点评,审核器看不到那
// 些发言,就以「原消息未提供群友名单」为由拒发——回复本身完全有依据。
//
// 所以只让它判断看得见的东西:说话方式,以及回复与原消息之间的关系。事实
// 是否属实、细节是否有出处,不在它的能力范围内,明确划出去。
const proactiveReplyQualityPrompt = `你是主动回复的表达质量审核器。判断这条回复该不该发出去。

重要前提:你看不到群聊历史。回复是在完整上下文里生成的,里面提到的人名、昵称、
具体发言、事件细节、数字和结论,绝大多数来自你看不到的那些消息。
因此严禁以「原消息里没有这些信息」「无法核实」「可能是编造的」为由拒绝——
判断事实真伪不是你的职责,你没有依据可查。

只按下面这些看得见的维度判断:
- 说话方式:是不是自然的聊天口吻;有没有客服腔、说教味、过度正式或浮夸表演。
- 是否答非所问:回复和原消息完全对不上号才算,只是展开了新角度不算。
- 是否被截断:结尾停在半句上、括号或引号没闭合。
- 是否空洞:通篇套话、正确的废话、没有任何实际内容。
- 是否是不必要的插话:原消息根本没在跟人交流、或明显不需要任何人接话。

候选回复受长度上限约束:简短、只答要点、不展开举例都不是缺陷,
不要因为「不够详细」「没有列全」「缺少解释」而拒绝。
拿不准时倾向放行:主动回复的表达问题成本很低,误杀一条有依据的回复成本更高。

只输出一个合法 JSON 对象,不要输出 Markdown 或额外文字:
{"should_send":true,"confidence":0.96,"reason":"口吻自然,和原消息对得上"}

confidence 必须是 0 到 1 的数字,表示你对「这条回复的表达方式适合发出去」的信心。`

func (r *Runtime) judgeProactiveReplyQuality(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) error {
	threshold := cfg.ProactiveReplyThreshold
	if event.chatInReply {
		threshold = cfg.chatInSettings().Threshold
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultProactiveReplyThreshold
	}

	payload, err := json.Marshal(map[string]any{
		"original_message": strings.TrimSpace(readableEventText(event, input)),
		"candidate_reply":  strings.TrimSpace(reply),
	})
	if err != nil {
		return &proactiveReplyQualityRejectedError{reason: "主动回复答案审核上下文编码失败，已保持沉默"}
	}
	qualityCtx, cancel := context.WithTimeout(ctx, proactiveReplyQualityTimeout(cfg))
	defer cancel()
	raw, err := r.runLLMRouterProvider(qualityCtx, func(client LLMProvider) (string, error) {
		resp, generateErr := client.Generate(qualityCtx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: proactiveReplyQualityPrompt},
				{Role: llm.RoleUser, Content: "请审核以下主动回复：\n" + string(payload)},
			},
		})
		if generateErr != nil {
			return "", generateErr
		}
		return resp.Text, nil
	})
	if err != nil {
		return &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案审核失败，已保持沉默：%v", err)}
	}
	decision, ok := parseProactiveReplyQualityDecision(raw)
	if !ok {
		return &proactiveReplyQualityRejectedError{reason: "主动回复答案审核结果无法解析，已保持沉默"}
	}
	if !decision.ShouldSend || decision.Confidence < threshold {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "回复准确度不足"
		}
		return &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案未通过准确度审核：%s（置信度 %.0f%%，阈值 %.0f%%）", reason, decision.Confidence*100, threshold*100)}
	}
	return nil
}

func proactiveReplyQualityTimeout(cfg BotConfig) time.Duration {
	const budget = 30 * time.Second
	if cfg.RequestTimeout > 0 && cfg.RequestTimeout < budget {
		return cfg.RequestTimeout
	}
	return budget
}

func parseProactiveReplyQualityDecision(raw string) (proactiveReplyQualityDecision, bool) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return proactiveReplyQualityDecision{}, false
	}
	var payload struct {
		ShouldSend *bool    `json:"should_send"`
		Confidence *float64 `json:"confidence"`
		Reason     *string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil || payload.ShouldSend == nil || payload.Confidence == nil {
		return proactiveReplyQualityDecision{}, false
	}
	if *payload.Confidence < 0 || *payload.Confidence > 1 {
		return proactiveReplyQualityDecision{}, false
	}
	decision := proactiveReplyQualityDecision{ShouldSend: *payload.ShouldSend, Confidence: *payload.Confidence}
	if payload.Reason != nil {
		decision.Reason = strings.TrimSpace(*payload.Reason)
	}
	return decision, true
}
