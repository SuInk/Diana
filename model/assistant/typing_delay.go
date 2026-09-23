package assistant

import (
	"strings"
	"time"
	"unicode/utf8"
)

// 模拟打字延时：连发时下一条等多久按它的字数算，像真人一边打一边发。
//
// 只作用于第二条起的等待。第一条之前模型生成本身已经花了时间，再按字数压一遍只会
// 让回复更慢。分段发送间隔仍是下限——那是防风控的底线，不能因为下一条只有两个字
// 就连珠炮似地发出去；上限封住长消息，一段几百字的说明不该让人干等半分钟。
const (
	defaultTypingDelayPerCharMS = 100
	maxTypingDelayPerCharMS     = 1000
	maxTypingDelay              = 6 * time.Second
)

// chunkSendInterval 返回发送 next 之前要等的时长。
func chunkSendInterval(cfg BotConfig, next string) time.Duration {
	interval := time.Duration(cfg.SendChunkIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = sendChunkInterval
	}
	if !boolValue(cfg.TypingDelayEnabled, false) {
		return interval
	}
	perChar := cfg.TypingDelayPerCharMS
	if perChar <= 0 {
		perChar = defaultTypingDelayPerCharMS
	}
	typing := min(time.Duration(utf8.RuneCountInString(strings.TrimSpace(next))*perChar)*time.Millisecond, maxTypingDelay)
	return max(interval, typing)
}
