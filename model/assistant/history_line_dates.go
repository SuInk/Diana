package assistant

import (
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 历史行的日期只在一份历史的第一行和换天的那一行写全，其余只写时分秒：
//
//	[历史 2026-11-11 23:59:10] 张三（im_user_a）: …
//	[历史 23:59:40] 李四（im_user_b）: …
//	[历史 2026-11-12 00:00:05] 张三（im_user_a）: …
//
// 几百条历史里同一个日期重复几百遍，每行白付十来个 token。参考的雨夹雪是换天时单独
// 插一行「--- 日期 ---」；这里改在换天那一行自己写全，效果一样，不多一条消息，也不用
// 让续接、预裁剪去认一种新的分隔消息。
//
// 渲染时（historyLinePrefix）每行照旧写全日期，渲染缓存按条目自己的源数据算，不依赖
// 前一行；省不省日期在拼成一份历史之后统一决定（compactHistoryLineDates）。这样同一批
// 输入的结果只由顺序决定，缓存条目也不会因为前一条被删了而写错。日期字符串是
// historyLinePrefix 按机器人时区格式化好的，这里只比字符串，不再换算时区。

// historyLineDateLabel 是本群历史行的开头。跨群历史（「[跨群历史 …]」）不走这套：
// 它们按话题检索、零散插在尾部，没有「前一行」可言，照旧写全日期。
const historyLineDateLabel = "[历史 "

// splitHistoryLineTime 拆开历史行开头的时间：date 为空表示这一行只写了时分秒。
// rest 从时间后面的「]」起。不是带时间的本群历史行时 ok 为 false。
func splitHistoryLineTime(content string) (date, clock, rest string, ok bool) {
	if !strings.HasPrefix(content, historyLineDateLabel) {
		return "", "", "", false
	}
	body := content[len(historyLineDateLabel):]
	end := strings.IndexByte(body, ']')
	if end < 0 {
		return "", "", "", false
	}
	stamp, rest := body[:end], body[end:]
	switch len(stamp) {
	case len("2006-01-02 15:04:05"):
		if _, err := time.Parse("2006-01-02 15:04:05", stamp); err != nil {
			return "", "", "", false
		}
		return stamp[:len("2006-01-02")], stamp[len("2006-01-02 "):], rest, true
	case len("15:04:05"):
		if _, err := time.Parse("15:04:05", stamp); err != nil {
			return "", "", "", false
		}
		return "", stamp, rest, true
	}
	return "", "", "", false
}

// historyLineDates 沿着一份历史从旧到新推日期，last 是上一条带时间的历史行的日期。
type historyLineDates struct {
	last string
}

// step 推进一行：返回这一行的日期（只写时分秒的行沿用上一行的）和它是否与上一行同一天。
func (d *historyLineDates) step(content string) (date, clock, rest string, sameDay, ok bool) {
	date, clock, rest, ok = splitHistoryLineTime(content)
	if !ok {
		return "", "", "", false, false
	}
	if date == "" {
		date = d.last
	}
	if date == "" {
		// 只写时分秒、前面又没有日期可沿用：推不出来，原样留着。
		return "", "", "", false, false
	}
	sameDay = date == d.last
	d.last = date
	return date, clock, rest, sameDay, true
}

// expand 返回这一行写全日期的形态。续接比对「这一行上文有没有」、预裁剪丢掉开头几行
// 之前，都先换成这个形态：同一条消息在不同的一份历史里前一行不同，省不省日期也就不同。
func (d *historyLineDates) expand(content string) string {
	date, clock, rest, _, ok := d.step(content)
	if !ok {
		return content
	}
	return historyLineDateLabel + date + " " + clock + rest
}

// compact 返回这一行在这份历史里该有的写法：和上一行同一天只写时分秒，否则写全日期。
// 传进来的行写没写全日期都可以。
func (d *historyLineDates) compact(content string) string {
	date, clock, rest, sameDay, ok := d.step(content)
	if !ok {
		return content
	}
	if sameDay {
		return historyLineDateLabel + clock + rest
	}
	return historyLineDateLabel + date + " " + clock + rest
}

// compactHistoryLineDates 把一份消息里的本群历史行按顺序改成省日期的写法，其余消息原样。
// 返回新切片，不改传进来的那份：stableGroupHistory 的条目缓存还握着它们。
func compactHistoryLineDates(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	var dates historyLineDates
	for index := range out {
		if out[index].Role == llm.RoleUser {
			out[index].Content = dates.compact(out[index].Content)
		}
	}
	return out
}
