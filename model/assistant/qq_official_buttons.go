// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// QQ 官方机器人能在 Markdown 消息底下挂一组按钮（keyboard），最多 5 排、每排 5 个。
// 按钮和表情包一样是「锦上添花」：让模型先加载工具再调用，它在聊天里几乎不会主动
// 去做，所以照 sticker 的做法挂成 agent_finalize 上的可选字段，收尾时顺手填。
//
// 只用两种按钮，都不需要处理点击回调：指令按钮点了就以用户身份发出 @机器人 + 内容，
// 机器人当成一条普通消息接着聊；链接按钮点了跳网页。
const (
	buttonsFinalizeFieldName = "buttons"

	qqKeyboardMaxRows      = 5
	qqKeyboardMaxPerRow    = 5
	qqButtonLabelMaxRunes  = 16
	qqButtonDataMaxRunes   = 100
	qqButtonActionLink     = 0
	qqButtonActionCommand  = 2
	qqButtonPermissionAll  = 2
	qqButtonStyleBlue      = 1
	qqButtonUnsupportedTip = "当前 QQ 版本不支持按钮，请升级后再试"
)

// OutgoingButton 是挂在消息底下的一个按钮。Link 为 true 时 Data 是要打开的网址，
// 否则是点了以后替用户发出的那句话。
type OutgoingButton struct {
	Label string
	Data  string
	Link  bool
}

func buttonsFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: buttonsFinalizeFieldName,
		// 「默认就填」是回放里压过来的：只写「适合时填」gpt-6-sol 0/36，这版 7/33，
		// 且拒答、更正、玩笑接话这些场合都没误填。
		Description: "QQ 官方机器人能在你这条回复底下挂几个按钮，对方点一下就替他发出那句话，比打字省事得多，所以默认就填：" +
			"只要对方接下来可能追问、挑选、继续或换个方向，就给 2～4 个按钮，写成对方最可能想接着说的话，" +
			"比如「展开讲讲」「那 B 呢」「再来一个」、你给出的几个选项、是/否。" +
			"每行一排，同一排用 | 分隔，每个写「显示文字」或「显示文字=点了替对方发的话」，打开网页写「显示文字=https://…」；" +
			"显示文字 2～8 个字。只有纯附和、发表情、对方明确在结束话题时才不填。",
	}
}

// offersButtonsFinalizeField 判断这一轮要不要给模型按钮字段：只有 QQ 官方机器人的
// 群聊和单聊能发，频道没开放原生 Markdown；配置成发纯文本时按钮也挂不上去。
func offersButtonsFinalizeField(cfg BotConfig, event MessageEvent) bool {
	if NormalizePlatformID(event.Platform) != PlatformQQOfficial || event.PlatformScope == "qq_guild" {
		return false
	}
	return !markdownToPlainForConfig(cfg)
}

// parseFinalizeButtons 把模型填的按钮文本解析成按钮行。写坏的项跳过，不让一个
// 格式错误连累整条回复。
func parseFinalizeButtons(value string) [][]OutgoingButton {
	var rows [][]OutgoingButton
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if len(rows) == qqKeyboardMaxRows {
			break
		}
		var row []OutgoingButton
		for _, item := range strings.Split(line, "|") {
			if len(row) == qqKeyboardMaxPerRow {
				break
			}
			label, data, hasData := strings.Cut(strings.TrimSpace(item), "=")
			label, data = strings.TrimSpace(label), strings.TrimSpace(data)
			if !hasData || data == "" {
				data = label
			}
			if label == "" {
				continue
			}
			button := OutgoingButton{Label: cutButtonText(label, qqButtonLabelMaxRunes), Data: cutButtonText(data, qqButtonDataMaxRunes)}
			lower := strings.ToLower(button.Data)
			button.Link = strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
			row = append(row, button)
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// cutButtonText 按字数截断按钮文字，不加省略号：按钮放不下多余的字。
func cutButtonText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// qqOfficialKeyboard 把按钮行转成开放平台的自定义 keyboard 结构。
//
// 一排里有两个以上指令按钮时，它们共用一个 group_id：群友实测同组按钮互斥，点了
// 一个其余就灰掉，正好是「选一个」的语义；链接按钮不参与。官方文档没写这个字段。
func qqOfficialKeyboard(rows [][]OutgoingButton) map[string]any {
	payloadRows := make([]any, 0, len(rows))
	for rowIndex, row := range rows {
		commands := 0
		for _, button := range row {
			if !button.Link {
				commands++
			}
		}
		buttons := make([]any, 0, len(row))
		for index, button := range row {
			action := map[string]any{
				"type":           qqButtonActionCommand,
				"data":           button.Data,
				"enter":          true,
				"unsupport_tips": qqButtonUnsupportedTip,
				"permission":     map[string]any{"type": qqButtonPermissionAll},
			}
			if button.Link {
				action = map[string]any{
					"type":           qqButtonActionLink,
					"data":           button.Data,
					"unsupport_tips": qqButtonUnsupportedTip,
					"permission":     map[string]any{"type": qqButtonPermissionAll},
				}
			}
			payload := map[string]any{
				"id":          fmt.Sprintf("%d-%d", rowIndex+1, index+1),
				"render_data": map[string]any{"label": button.Label, "visited_label": button.Label, "style": qqButtonStyleBlue},
				"action":      action,
			}
			if !button.Link && commands > 1 {
				payload["group_id"] = fmt.Sprintf("row%d", rowIndex+1)
			}
			buttons = append(buttons, payload)
		}
		payloadRows = append(payloadRows, map[string]any{"buttons": buttons})
	}
	return map[string]any{"content": map[string]any{"rows": payloadRows}}
}

type finalizeButtonsKey struct{}

// finalizeButtons 把收尾时填的按钮从 generateReply 带到发送阶段，挂在这一轮最后
// 一条文字消息上。只有主回复路径会在 ctx 里放它。
type finalizeButtons struct {
	mu   sync.Mutex
	rows [][]OutgoingButton
}

func withFinalizeButtons(ctx context.Context) (context.Context, *finalizeButtons) {
	holder := &finalizeButtons{}
	return context.WithValue(ctx, finalizeButtonsKey{}, holder), holder
}

func finalizeButtonsFromContext(ctx context.Context) *finalizeButtons {
	holder, _ := ctx.Value(finalizeButtonsKey{}).(*finalizeButtons)
	return holder
}

func (f *finalizeButtons) set(value string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.rows = parseFinalizeButtons(value)
	f.mu.Unlock()
}

// take 取出按钮并清空，同一轮只挂一次。
func (f *finalizeButtons) take() [][]OutgoingButton {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.rows
	f.rows = nil
	return rows
}
