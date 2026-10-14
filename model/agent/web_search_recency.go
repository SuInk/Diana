// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// maximumWebSearchRecencyDays 是 recency 的上限，再长就等于不过滤。
const maximumWebSearchRecencyDays = 3650

type webSearchRecencyKey struct{}

// withWebSearchRecency 把模型给的「只要最近 N 天」带进一次搜索。对应 Codex web.run
// 的 recency：问「出了没有／最新是哪个」时，限定近期能把发布前的旧爆料挤出候选。
// 走 context 而不是改查询词：时间过滤是引擎的地址参数，写进查询词反而会被当关键词。
func withWebSearchRecency(ctx context.Context, days int) context.Context {
	if days <= 0 {
		return ctx
	}
	return context.WithValue(ctx, webSearchRecencyKey{}, min(days, maximumWebSearchRecencyDays))
}

func webSearchRecency(ctx context.Context) int {
	days, _ := ctx.Value(webSearchRecencyKey{}).(int)
	return days
}

// applySearchEngineRecency 给内置引擎的结果页地址加上时间过滤。各家只认固定档位
// （天/周/月/年）时向上取最近的一档，宁可多放几天也不漏掉要找的页面；自定义引擎
// 不知道参数名，原样返回。
func applySearchEngineRecency(tool, target string, days int, now time.Time) string {
	if days <= 0 {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	values := u.Query()
	bucket := func(day, week, month, year string) string {
		switch {
		case days <= 1:
			return day
		case days <= 7:
			return week
		case days <= 31:
			return month
		default:
			return year
		}
	}
	switch tool {
	case "google":
		if days > 366 {
			return target
		}
		values.Set("tbs", "qdr:"+bucket("d", "w", "m", "y"))
	case "bing":
		if days <= 31 {
			values.Set("filters", fmt.Sprintf(`ex1:"%s"`, bucket("ez1", "ez2", "ez3", "")))
		} else {
			// Bing 的自定义区间以 1970-01-01 起的天数计。
			to := int(now.Unix() / 86400)
			values.Set("filters", fmt.Sprintf(`ex1:"ez5_%d_%d"`, to-days, to))
		}
	case "duckduckgo":
		if days > 366 {
			return target
		}
		values.Set("df", bucket("d", "w", "m", "y"))
	case "baidu":
		from := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
		values.Set("gpc", "stf="+strconv.FormatInt(from, 10)+","+strconv.FormatInt(now.Unix(), 10)+"|stftype=1")
	default:
		return target
	}
	u.RawQuery = values.Encode()
	return u.String()
}
