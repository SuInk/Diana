// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// resolverCount 是平台接口里的计数。各家格式不一：B 站、抖音给整数，
// 小红书给「1.2万」这样的字符串，微博超过阈值时给「100万+」。数字统一
// 换成中文单位，平台自己格式化好的字符串原样保留。
type resolverCount string

// UnmarshalJSON 从不报错：计数只是附带信息，某个接口把同名字段换成数组或
// 对象时，不能连累整条内容解析失败。
func (c *resolverCount) UnmarshalJSON(data []byte) error {
	*c = ""
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	switch {
	case data[0] == '"':
		var text string
		if json.Unmarshal(data, &text) == nil {
			*c = resolverCountFromString(text)
		}
	case data[0] == '-' || (data[0] >= '0' && data[0] <= '9'):
		var number json.Number
		if json.Unmarshal(data, &number) == nil {
			*c = resolverCountFromString(number.String())
		}
	}
	return nil
}

func resolverCountFromString(text string) resolverCount {
	text = strings.TrimSpace(text)
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return resolverCountFromInt(int64(value))
	}
	return resolverCount(text)
}

// resolverCountFromInt 把计数格式化成群里习惯的写法：一万以下原样，
// 往上用「万」「亿」保留一位小数。0 和负数视为平台没给。
func resolverCountFromInt(value int64) resolverCount {
	switch {
	case value <= 0:
		return ""
	case value < 10_000:
		return resolverCount(strconv.FormatInt(value, 10))
	case value < 100_000_000:
		return resolverCount(resolverCountDecimal(float64(value)/10_000) + "万")
	default:
		return resolverCount(resolverCountDecimal(float64(value)/100_000_000) + "亿")
	}
}

func resolverCountDecimal(value float64) string {
	// 截断而不是四舍五入：9999 万不能显示成「10000.0万」。
	text := strconv.FormatFloat(float64(int64(value*10))/10, 'f', 1, 64)
	return strings.TrimSuffix(text, ".0")
}

type resolverStat struct {
	Label string
	Count resolverCount
}

// resolverStatsLine 拼成一行「播放 12.3万 · 点赞 4567 · 收藏 89」，
// 平台没给的项跳过；全都没有时返回空串，调用方不加这一行。
func resolverStatsLine(stats ...resolverStat) string {
	parts := make([]string, 0, len(stats))
	for _, stat := range stats {
		count := strings.TrimSpace(string(stat.Count))
		if count == "" || count == "0" {
			continue
		}
		parts = append(parts, stat.Label+" "+count)
	}
	return strings.Join(parts, " · ")
}

func appendResolverStatsLine(text string, stats ...resolverStat) string {
	line := resolverStatsLine(stats...)
	if line == "" {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return line
	}
	return text + "\n" + line
}
