// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// DecisionSpec 描述一次结构化判断：问哪几个问题，答案分别写进输出 JSON 的哪个字段。
//
// 会生成文本的供应商忽略它——契约已经写在提示词里了。只做判断的供应商（Jev 这类
// System One 模型）不生成文本，靠这张表把带概率的答案还原成调用方本来就在解析的
// 那个 JSON 对象，上层的解析、阈值和日志因此一行都不用改。
type DecisionSpec struct {
	Questions []DecisionQuestion
}

type DecisionKind string

const (
	DecisionNoul   DecisionKind = "noul"
	DecisionChoice DecisionKind = "choice"
	DecisionScore  DecisionKind = "score"
)

type DecisionOption struct {
	Value       string
	Description string
}

// DecisionQuestion 是一道判断题及其回填位置。Path 用点分写法指向输出对象里的字段，
// 例如 "relevance.directed"。
type DecisionQuestion struct {
	Key          string
	Kind         DecisionKind
	Label        string
	Instructions string

	// noul
	TrueCriteria  string
	FalseCriteria string
	// Threshold 是 noul 判「是」的最低概率，零值按 0.5。判断模型对拿不准的题给的
	// 就是 0.5 上下——线上「在跟机器人说话」判是的里七成落在 0.5 到 0.69——按 0.5
	// 切，拿不准的一半会被当成确定的是。调用方把这道题的语义定成「拿不准算否」时，
	// 把刀挪到两堆概率之间的空档上。
	Threshold float64
	// choice
	Options []DecisionOption
	// EmptyOption 指定一个表示「没有」的选项，选中它时写进输出的是空字符串。
	EmptyOption string
	// score：从低到高的锚点描述，2 到 10 档。返回值按档位下标归一化到 Min~Max；
	// 锚点在刻度上不是等距时用 LevelValues 逐档写出实际分值，档位之间线性插值。
	Levels      []string
	LevelValues []float64
	Min         float64
	Max         float64
	Decimals    int

	Path string
	// ConfidencePath 收结论的置信度：noul 取「站在结论这边」的概率，其余取 confidence。
	ConfidencePath string
	// ReasonPath 收合成的理由。判断模型不写字，这里给的是「结论 + 概率」，
	// 够日志复盘用，也满足上层「reason 不得为空」的校验。
	ReasonPath string
	// AppendValue 非空时这道题是多选的一项：答 true 才把它追加进 Path 指向的数组。
	AppendValue string
}

// DecisionAnswer 是供应商对一道题的回答。
type DecisionAnswer struct {
	Kind       DecisionKind
	Noul       float64
	Choice     string
	Score      float64
	Confidence float64
}

// Validate 在发请求前检查这张表自己是不是自洽的，免得把明显写错的题目发到上游。
func (s DecisionSpec) Validate() error {
	if len(s.Questions) == 0 {
		return fmt.Errorf("llm: decision spec has no questions")
	}
	seen := map[string]bool{}
	for _, q := range s.Questions {
		key := strings.TrimSpace(q.Key)
		if key == "" {
			return fmt.Errorf("llm: decision question key is required")
		}
		if seen[key] {
			return fmt.Errorf("llm: duplicate decision question %q", key)
		}
		seen[key] = true
		switch q.Kind {
		case DecisionNoul:
			if q.Threshold < 0 || q.Threshold >= 1 {
				return fmt.Errorf("llm: decision question %q threshold %v must stay within [0, 1)", key, q.Threshold)
			}
		case DecisionChoice:
			if len(q.Options) < 2 || len(q.Options) > 255 {
				return fmt.Errorf("llm: decision question %q needs 2 to 255 options", key)
			}
		case DecisionScore:
			if len(q.Levels) < 2 || len(q.Levels) > 10 {
				return fmt.Errorf("llm: decision question %q needs 2 to 10 score levels", key)
			}
		default:
			return fmt.Errorf("llm: decision question %q has unknown kind %q", key, q.Kind)
		}
		if strings.TrimSpace(q.Path) == "" {
			return fmt.Errorf("llm: decision question %q has no output path", key)
		}
	}
	return nil
}

func (q DecisionQuestion) noulThreshold() float64 {
	if q.Threshold > 0 {
		return q.Threshold
	}
	return 0.5
}

// RenderDecisionAnswers 把答案按 Path 摆回调用方期待的 JSON 对象。
func (s DecisionSpec) RenderDecisionAnswers(answers map[string]DecisionAnswer) (string, error) {
	root := map[string]any{}
	// 多选题先把空数组放好：一条都没命中时字段仍然存在，上层不必区分「没有」和「没答」。
	for _, q := range s.Questions {
		if q.AppendValue != "" {
			if _, err := ensureArray(root, q.Path); err != nil {
				return "", err
			}
		}
	}
	for _, q := range s.Questions {
		answer, ok := answers[q.Key]
		if !ok {
			return "", fmt.Errorf("llm: decision answer %q is missing", q.Key)
		}
		if err := s.applyAnswer(root, q, answer); err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s DecisionSpec) applyAnswer(root map[string]any, q DecisionQuestion, answer DecisionAnswer) error {
	confidence := answer.Confidence
	var reason string
	switch q.Kind {
	case DecisionNoul:
		value := answer.Noul >= q.noulThreshold()
		confidence = answer.Noul
		if !value {
			confidence = 1 - answer.Noul
		}
		if q.AppendValue != "" {
			if value {
				if err := appendValue(root, q.Path, q.AppendValue); err != nil {
					return err
				}
			}
		} else if err := setPath(root, q.Path, value); err != nil {
			return err
		}
		verdict := "否"
		if value {
			verdict = "是"
		}
		reason = fmt.Sprintf("%s：%s（%.2f）", q.label(), verdict, answer.Noul)
	case DecisionChoice:
		choice := answer.Choice
		if q.EmptyOption != "" && choice == q.EmptyOption {
			choice = ""
		}
		if err := setPath(root, q.Path, choice); err != nil {
			return err
		}
		reason = fmt.Sprintf("%s：%s（%.2f）", q.label(), answer.Choice, confidence)
	case DecisionScore:
		value := q.scoreValue(answer.Score)
		if err := setPath(root, q.Path, value); err != nil {
			return err
		}
		reason = fmt.Sprintf("%s：%.2f（%.2f）", q.label(), value, confidence)
	}
	if path := strings.TrimSpace(q.ConfidencePath); path != "" {
		if err := setPath(root, path, round(confidence, 2)); err != nil {
			return err
		}
	}
	if path := strings.TrimSpace(q.ReasonPath); path != "" {
		if err := appendReason(root, path, reason); err != nil {
			return err
		}
	}
	return nil
}

func (q DecisionQuestion) label() string {
	if label := strings.TrimSpace(q.Label); label != "" {
		return label
	}
	return q.Key
}

// scoreValue 把档位下标换算回调用方的刻度。上游返回的是按概率加权的档位均值，
// 直接当分数用会把 0~1 的闲聊分变成 0~4。
func (q DecisionQuestion) scoreValue(score float64) float64 {
	min, max := q.Min, q.Max
	if min == 0 && max == 0 {
		max = 1
	}
	span := float64(len(q.Levels) - 1)
	if span <= 0 {
		return round(min, q.decimals())
	}
	if len(q.LevelValues) == len(q.Levels) {
		return round(interpolateLevels(q.LevelValues, score), q.decimals())
	}
	ratio := score / span
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return round(min+ratio*(max-min), q.decimals())
}

func (q DecisionQuestion) decimals() int {
	if q.Decimals > 0 {
		return q.Decimals
	}
	return 2
}

// interpolateLevels 在锚点之间线性插值。上游给的是按概率加权的档位均值，落在两档
// 之间时得按这两档的实际分值取中，不能当成等距刻度。
func interpolateLevels(values []float64, score float64) float64 {
	if score <= 0 {
		return values[0]
	}
	last := float64(len(values) - 1)
	if score >= last {
		return values[len(values)-1]
	}
	lower := int(math.Floor(score))
	fraction := score - float64(lower)
	return values[lower] + fraction*(values[lower+1]-values[lower])
}

func round(value float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Round(value*scale) / scale
}

func decisionPathParts(path string) []string {
	parts := strings.Split(path, ".")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containerFor(root map[string]any, parts []string) (map[string]any, error) {
	node := root
	for _, part := range parts[:len(parts)-1] {
		child, ok := node[part]
		if !ok {
			created := map[string]any{}
			node[part] = created
			node = created
			continue
		}
		nested, ok := child.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("llm: decision path %q collides with a value", part)
		}
		node = nested
	}
	return node, nil
}

func setPath(root map[string]any, path string, value any) error {
	parts := decisionPathParts(path)
	if len(parts) == 0 {
		return fmt.Errorf("llm: decision path is empty")
	}
	node, err := containerFor(root, parts)
	if err != nil {
		return err
	}
	node[parts[len(parts)-1]] = value
	return nil
}

func ensureArray(root map[string]any, path string) ([]string, error) {
	parts := decisionPathParts(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("llm: decision path is empty")
	}
	node, err := containerFor(root, parts)
	if err != nil {
		return nil, err
	}
	key := parts[len(parts)-1]
	current, ok := node[key]
	if !ok {
		created := []string{}
		node[key] = created
		return created, nil
	}
	values, ok := current.([]string)
	if !ok {
		return nil, fmt.Errorf("llm: decision path %q is not a list", path)
	}
	return values, nil
}

func appendValue(root map[string]any, path, value string) error {
	values, err := ensureArray(root, path)
	if err != nil {
		return err
	}
	parts := decisionPathParts(path)
	node, err := containerFor(root, parts)
	if err != nil {
		return err
	}
	node[parts[len(parts)-1]] = append(values, value)
	return nil
}

func appendReason(root map[string]any, path, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return nil
	}
	parts := decisionPathParts(path)
	node, err := containerFor(root, parts)
	if err != nil {
		return err
	}
	key := parts[len(parts)-1]
	if existing, ok := node[key].(string); ok && strings.TrimSpace(existing) != "" {
		node[key] = existing + "；" + reason
		return nil
	}
	node[key] = reason
	return nil
}

// decisionQuestionKeys 给日志和错误信息一个稳定顺序。
func decisionQuestionKeys(answers map[string]DecisionAnswer) []string {
	keys := make([]string, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
