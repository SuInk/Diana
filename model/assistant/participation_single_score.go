package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SuInk/diana/model/applog"
	"strings"
)

type participationRating struct {
	Score  *float64 `json:"score"`
	Reason string   `json:"reason"`
}
type participationRatings struct {
	Relevance     participationRating `json:"relevance"`
	ChatIn        participationRating `json:"chat_in"`
	Answerability participationRating `json:"answerability"`
}

// parseParticipationRatings 宽松解析接话评分。线上 7.8% 的评分被整条丢弃，原因是模型
// 在一个合法 JSON 对象后面多吐了几个字符，或者输出被截断在最后一个右花括号之前。这里
// 允许代码围栏、前后多余文字和未知字段，并在三项评分都已写完时补齐缺失的右花括号；
// 分数必须落在 0 到 1、原因不得为空的校验保持不变，修不回来的输出仍算解析失败。
func parseParticipationRatings(raw string) (participationRatings, error) {
	var p participationRatings
	text, err := participationRatingsJSON(raw)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return p, err
	}
	for _, v := range []participationRating{p.Relevance, p.ChatIn, p.Answerability} {
		if v.Score == nil || *v.Score < 0 || *v.Score > 1 || strings.TrimSpace(v.Reason) == "" {
			return p, fmt.Errorf("each rating needs a score from 0 to 1 and a reason")
		}
	}
	return p, nil
}

// participationRatingsJSON 取出输出里第一个配平的顶层 JSON 对象，忽略它后面的任何字符。
// 对象始终没有闭合时按截断处理：三项评分都已出现才补齐右花括号，否则判为解析失败。
func participationRatingsJSON(raw string) (string, error) {
	text := strings.TrimSpace(stripJSONCodeFence(strings.TrimSpace(raw)))
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", fmt.Errorf("rating output has no JSON object")
	}
	// 清噪从第一个左花括号开始，不从整段开始：对象前面可能有模型自己写的说明文字，
	// 里面的半角引号会把字符串状态带偏；从对象开头起才确定是 JSON 语法。
	text = participationRatingsSanitize(text[start:])
	depth, inString, escaped := 0, false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case inString && c == '"':
			inString = false
		case inString:
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return text[:i+1], nil
			}
		}
	}
	if inString || depth <= 0 {
		return "", fmt.Errorf("rating output is not a complete JSON object")
	}
	if participationRatingsLookComplete(text) {
		return text + strings.Repeat("}", depth), nil
	}
	return "", fmt.Errorf("rating output was truncated before all three ratings")
}

// participationRatingsSanitize 丢掉 JSON 字符串之外那些语法上不可能出现的字符。
//
// 中转链路会在结构字符之间塞进零宽字符或整段乱码，线上抓到过 U+200C、古吉拉特语
// 音节和「】【。」，位置几乎总是压在最后一个右花括号上：要么把它顶掉，要么夹在
// 最后两个花括号之间。两种都让整条评分被判为格式无效，而模型给出的三项分数其实
// 是完整的。字符串内部一个字节都不动，reason 里的中文、标点和表情原样保留。
func participationRatingsSanitize(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case inString && c == '"':
			inString = false
		case inString:
		case c == '"':
			inString = true
		default:
			if !participationJSONSyntaxByte(c) {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.String()
}

// participationJSONSyntaxByte 报告某个字节能否出现在 JSON 字符串之外。
// 多字节字符的每个字节都不在这张表里，所以整字符会被一起丢掉。
func participationJSONSyntaxByte(c byte) bool {
	switch c {
	case '{', '}', '[', ']', ':', ',', ' ', '\t', '\n', '\r', '-', '.', 'E':
		return true
	}
	if c >= '0' && c <= '9' {
		return true
	}
	// true、false、null 用到的字母，'e' 同时也是指数记号。
	return strings.IndexByte("truefalsn", c) >= 0
}

// participationRatingsLookComplete 判断被截断的输出里三项评分是否都已经带上了分数字段。
func participationRatingsLookComplete(text string) bool {
	for _, key := range []string{`"relevance"`, `"answerability"`, `"chat_in"`} {
		if !strings.Contains(text, key) {
			return false
		}
	}
	return strings.Count(text, `"score"`) >= 3
}

func validParticipationLevel(s string) bool {
	switch s {
	case "off", "minimal", "low", "medium", "high", "extreme", "always":
		return true
	}
	return false
}
func (p ParticipationPreferences) ratingLevels() (string, string) {
	r, c := p.RelevanceLevel, p.ChatLevel
	if !validParticipationLevel(r) {
		r = "medium"
		if p.replyLevel() == ChatInLevelOff {
			r = "off"
		}
	}
	if !validParticipationLevel(c) {
		c = string(p.replyLevel())
		if c == "max" {
			c = "always"
		}
		if !validParticipationLevel(c) {
			c = "low"
		}
	}
	return r, c
}
func ratingPasses(score float64, level string) bool {
	if level == "off" {
		return false
	}
	if level == "always" {
		return true
	}
	threshold, valid := map[string]float64{"minimal": 0.90, "low": 0.70, "medium": 0.50, "high": 0.30, "extreme": 0.10}[level]
	if !valid {
		return false
	}
	return score > 0 && score >= threshold
}
func (p ParticipationPreferences) ratingsAllow(v participationRatings, cooldown bool) (bool, bool) {
	if v.Answerability.Score == nil {
		return false, false
	}
	if level := p.answerabilityLevel(); level != "off" && !ratingPasses(*v.Answerability.Score, level) {
		return false, false
	}
	r, c := p.ratingLevels()
	// A zero in both dimensions encodes stop/repetition; always does not override it.
	if *v.Relevance.Score == 0 && *v.ChatIn.Score == 0 {
		return false, false
	}
	related := ratingPasses(*v.Relevance.Score, r)
	chat := ratingPasses(*v.ChatIn.Score, c) && cooldown
	return related || chat, !related && chat
}

func (p ParticipationPreferences) answerabilityLevel() string {
	if validParticipationLevel(p.AnswerabilityLevel) {
		return p.AnswerabilityLevel
	}
	return "medium"
}

func (r *Runtime) recordParticipationRatings(ctx context.Context, event MessageEvent, v participationRatings, parsed, allowed, retried bool, cfg BotConfig, raw string) {
	w := r.appLogWriter()
	if w == nil {
		return
	}
	a, b := cfg.participationPreferences().ratingLevels()
	_ = w.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "diana.proactive_reply_route", Message: "模型已完成接话评分", Actor: oneBotEventActor(event), Target: event.MessageID, Metadata: map[string]any{"group_id": event.GroupID, "ratings": v, "relevance_level": a, "chat_level": b, "answerability_level": cfg.participationPreferences().answerabilityLevel(), "parsed": parsed, "allowed": allowed, "retried": retried, "reason": event.routingReason, "raw": truncateRunesFromStart(raw, 1000)}})
}
