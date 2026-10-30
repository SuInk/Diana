// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package secretmask

import (
	"fmt"
	"regexp"
	"strings"
)

// 隐私代理把密钥和个人信息换成 pv_<类别>_<12 位十六进制> 交给模型，执行工具前再换回
// 原值（model/assistant/llm_secret_privacy.go）。格式定在这里，是因为 Agent 执行层
// 也要认它：还原得了的在执行前都已换回，入参里还剩着的就是还原不了的。

// PlaceholderPrefix 是占位符的共同前缀。
const PlaceholderPrefix = "pv_"

// PlaceholderKinds 是占位符里会出现的全部类别。
var PlaceholderKinds = []string{"api_key", "private_key", "secret", "id_card", "bank_card", "phone", "email"}

var placeholderKindAlternation = strings.Join(PlaceholderKinds, "|")

// PlaceholderPattern 认出一个完整的占位符。后面紧跟字母数字的不算：多抄了几位的
// 占位符是另一个值，不能当成前 12 位那个还原。
var PlaceholderPattern = regexp.MustCompile(`\b` + PlaceholderPrefix + `(?:` + placeholderKindAlternation + `)_[0-9a-f]{12}\b`)

// leftoverPlaceholderPattern 认出长得像占位符的残片，抄短、抄长、大小写写错的都算。
var leftoverPlaceholderPattern = regexp.MustCompile(`(?i)\b` + PlaceholderPrefix + `(?:` + placeholderKindAlternation + `)_[0-9a-z]*`)

// UnresolvedPlaceholderError 在工具入参里找还剩着的占位符。
//
// 能还原的占位符在模型返回时就已换回原值，到这里还剩下的只可能是模型抄错或编出来
// 的。放它过去，命令会拿着一串错的值去请求：以前模型重抄密钥时漏了一个字符，接口
// 回 401，模型就告诉用户「密钥可能被撤销了」。这里直接拦下，让模型照原样再抄一遍。
func UnresolvedPlaceholderError(input map[string]any) error {
	var found string
	var walk func(value any)
	walk = func(value any) {
		if found != "" {
			return
		}
		switch typed := value.(type) {
		case string:
			found = leftoverPlaceholderPattern.FindString(typed)
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case []string:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(input)
	if found == "" {
		return nil
	}
	return fmt.Errorf("参数里的占位符 %s 还原不出原值，没有执行：占位符必须从上下文里逐字照抄，不能改写、截断或自己拼", found)
}
