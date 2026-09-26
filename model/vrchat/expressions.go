// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package vrchat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/osc"
)

// 心情档位在映射表里对应的表情名。机器人的心情只有三档（见 assistant/mood.go），
// 映射表写了同名条目才会被心情驱动，没写就不动 Avatar。
const (
	ExpressionNeutral = "平静"
	ExpressionHappy   = "开心"
	ExpressionLow     = "低落"
)

// DefaultExpressionMap 是插件设置里的默认模板。参数名只是示例：每个 Avatar 的
// 参数都是作者自己起的，用户得照着自己 Avatar 的 Expression Parameters 改。
const DefaultExpressionMap = `# 每行一个表情：名称 = 参数:值，多个参数用逗号隔开，名称可用 | 写别名
# 值写 true/false 是 Bool，整数是 Int（0-255），带小数点是 Float（-1 到 1）
# 参数名必须和 Avatar 的 Expression Parameters 一致；「平静」是复位用的默认表情
平静|neutral = Expression:0, Slump:false
开心|happy = Expression:1
疑惑|confused = Expression:2
屑|smug = Expression:3
低落|sad = Expression:4
趴桌|slump = Slump:true`

// Param 是一个要写进 Avatar 的参数值，Value 只会是 bool、int32 或 float32。
type Param struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// Expression 是映射表里的一行。
type Expression struct {
	Names  []string `json:"names"`
	Params []Param  `json:"params"`
}

// Name 返回主名称（第一个名字）。
func (e Expression) Name() string {
	if len(e.Names) == 0 {
		return ""
	}
	return e.Names[0]
}

// ExpressionMap 是解析好的「表情名 → 参数」映射，保留书写顺序。
type ExpressionMap struct {
	entries []Expression
	index   map[string]int
}

// ParseExpressionMap 解析映射文本。坏行跳过并给出说明，而不是整张表作废：
// 用户改错一行，别的表情不该跟着失灵。
func ParseExpressionMap(text string) (ExpressionMap, []string) {
	out := ExpressionMap{index: map[string]int{}}
	var problems []string
	for number, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.NewReplacer("＝", "=", "｜", "|", "，", ",", "：", ":").Replace(line)
		left, right, ok := strings.Cut(line, "=")
		if !ok {
			problems = append(problems, fmt.Sprintf("第 %d 行缺少「=」", number+1))
			continue
		}
		var names []string
		for _, name := range strings.Split(left, "|") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			problems = append(problems, fmt.Sprintf("第 %d 行没有表情名", number+1))
			continue
		}
		var params []Param
		bad := false
		for _, item := range strings.Split(right, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			param, err := parseParam(item)
			if err != nil {
				problems = append(problems, fmt.Sprintf("第 %d 行：%v", number+1, err))
				bad = true
				break
			}
			params = append(params, param)
		}
		if bad {
			continue
		}
		if len(params) == 0 {
			problems = append(problems, fmt.Sprintf("第 %d 行没有参数", number+1))
			continue
		}
		position := len(out.entries)
		out.entries = append(out.entries, Expression{Names: names, Params: params})
		for _, name := range names {
			key := expressionKey(name)
			if _, exists := out.index[key]; exists {
				problems = append(problems, fmt.Sprintf("第 %d 行：表情名「%s」重复，沿用前面的定义", number+1, name))
				continue
			}
			out.index[key] = position
		}
	}
	return out, problems
}

func parseParam(item string) (Param, error) {
	name, rawValue, ok := strings.Cut(item, ":")
	name, rawValue = strings.TrimSpace(name), strings.TrimSpace(rawValue)
	if !ok || name == "" || rawValue == "" {
		return Param{}, fmt.Errorf("「%s」应写成 参数名:值", item)
	}
	if strings.Contains(name, "/") {
		return Param{}, fmt.Errorf("参数名「%s」不能包含 /", name)
	}
	if err := osc.ValidateAddress(ParameterAddress(name)); err != nil {
		return Param{}, fmt.Errorf("参数名「%s」含有 OSC 不允许的字符", name)
	}
	switch strings.ToLower(rawValue) {
	case "true":
		return Param{Name: name, Value: true}, nil
	case "false":
		return Param{Name: name, Value: false}, nil
	}
	if strings.ContainsAny(rawValue, ".eE") {
		number, err := strconv.ParseFloat(rawValue, 32)
		if err != nil {
			return Param{}, fmt.Errorf("「%s」的值不是数字", name)
		}
		if number < -1 || number > 1 {
			return Param{}, fmt.Errorf("「%s」是 Float 参数，值要在 -1 到 1 之间", name)
		}
		return Param{Name: name, Value: float32(number)}, nil
	}
	number, err := strconv.Atoi(rawValue)
	if err != nil {
		return Param{}, fmt.Errorf("「%s」的值应为 true/false、整数或小数", name)
	}
	if number < 0 || number > 255 {
		return Param{}, fmt.Errorf("「%s」是 Int 参数，值要在 0 到 255 之间", name)
	}
	return Param{Name: name, Value: int32(number)}, nil
}

func expressionKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Lookup 按名称或别名查表情，大小写不敏感。
func (m ExpressionMap) Lookup(name string) (Expression, bool) {
	position, ok := m.index[expressionKey(name)]
	if !ok {
		return Expression{}, false
	}
	return m.entries[position], true
}

// Entries 返回全部表情，顺序同映射文本。
func (m ExpressionMap) Entries() []Expression {
	return append([]Expression(nil), m.entries...)
}

// Names 返回每个表情的主名称。
func (m ExpressionMap) Names() []string {
	names := make([]string, 0, len(m.entries))
	for _, entry := range m.entries {
		names = append(names, entry.Name())
	}
	return names
}

// ParameterAddress 返回 Avatar 参数的 OSC 地址。
func ParameterAddress(name string) string { return "/avatar/parameters/" + name }
