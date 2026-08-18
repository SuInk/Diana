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

const proactiveReplyQualityPrompt = `你是一个严格的主动回复答案质量审核器。
请判断候选回复是否准确回答了原始消息，是否与上下文相关，是否包含明显幻觉、答非所问、无依据断言、空泛套话或不必要的插话。
只有候选回复确实有实质内容、与原消息匹配且没有明显错误时才允许发送。
主动回复宁可沉默，也不要发送不确定的答案。
候选回复受长度上限约束：简短、只答要点、没有展开举例都不算缺陷，不要因为“不够详细”“没有列全”而拒绝；只有它被切在半句上、答非所问、明显错误或空洞无内容时才拒绝。

只输出一个合法 JSON 对象，不要输出 Markdown 或额外文字：
{"should_send":true,"confidence":0.96,"reason":"回答直接且与原问题匹配"}

confidence 必须是 0 到 1 的数字，表示你对“这条回复值得发送且内容可靠”的信心。`

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
