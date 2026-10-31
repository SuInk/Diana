// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"

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
	qqButtonActionCallback = 1
	qqButtonActionCommand  = 2
	qqButtonPermissionAll  = 2
	qqButtonStyleGray      = 0
	qqButtonStyleBlue      = 1
	qqButtonStyleRed       = 3
	qqButtonStyleBlueFill  = 4
	qqButtonUnsupportedTip = "当前 QQ 版本不支持按钮，请升级后再试"

	// QQ 把一排的宽度平分给这排的按钮，字放不下就截成「…」。宽度按半角计（汉字
	// 算 2）：一排约 36 个半角宽，每个按钮的边框和内边距再吃掉约 4 个。对着桌面 QQ
	// 截图量的：三个一排时四个字的「太哲学了」放得下，再长就截成「那你会选…」；手机更窄，
	// 所以取偏紧的值。
	qqKeyboardRowWidth   = 36
	qqButtonPaddingWidth = 4

	// qqButtonCallbackPrefix 写在按钮数据前，表示用回调按钮：点了不替对方发话，
	// 平台推 INTERACTION_CREATE，数据当成对方说的话交给模型。
	qqButtonCallbackPrefix = "回调:"
)

// 按钮样式和字号都由源码定好，模型在 buttons 里按用途挑，请求体由 Diana 拼，升级时
// 跟着源码更新。模型写的是语义（主要、危险、次要），颜色在这里定，和 OpenClaw 的
// presentation 按钮 style 一个思路。开放平台文档只写了 style 0 灰色线框、1 蓝色线框；
// 3 红色字体、4 蓝色背景见官方 Go SDK botgo 的 RenderData 注释和腾讯的 openclaw-qqbot
// 类型定义；整组字号 keyboard.content.style.font_size（small / middle / large）见 botgo 的
// KeyboardStyle，Yunzai 的 QQBot-Plugin 等也这么发。
var (
	// qqButtonStylePrefixes 写在一项最前面标用途，不写是普通按钮（蓝色线框）。
	qqButtonStylePrefixes = map[string]int{
		"主要:": qqButtonStyleBlueFill, "主要：": qqButtonStyleBlueFill,
		"危险:": qqButtonStyleRed, "危险：": qqButtonStyleRed,
		"次要:": qqButtonStyleGray, "次要：": qqButtonStyleGray,
	}
	// qqKeyboardFontSizes 是单独一行写的整组字号，不写是平台默认大小。
	qqKeyboardFontSizes = map[string]string{"小号": "small", "中号": "middle", "大号": "large"}
)

// OutgoingButton 是挂在消息底下的一个按钮。Link 为 true 时 Data 是要打开的网址，
// 否则是点了以后替用户发出的那句话；Callback 为 true 时不替用户发话，Data 随回调
// 事件送回来。Group 相同的指令按钮互斥（见 qqOfficialKeyboard），为 0 时按所在的排分组。
// Style 是开放平台的 render_data.style，nil 是蓝色线框。
type OutgoingButton struct {
	Label    string
	Data     string
	Link     bool
	Callback bool
	Style    *int
	Group    int
}

func buttonsFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: buttonsFinalizeFieldName,
		// 「默认就填」是回放里压过来的：只写「适合时填」gpt-6-sol 0/36，这版 7/33，
		// 且拒答、更正、玩笑接话这些场合都没误填。
		Description: "QQ 官方机器人能在你这条回复底下挂几个按钮，对方点一下就替他发出那句话，所以默认就填：" +
			"只要对方接下来可能追问、挑选、继续或换个方向，就给 2～4 个按钮，写成对方最可能想接着说的话，" +
			"比如「展开讲讲」「那 B 呢」「再来一个」、你给的选项、是/否。" +
			"每行一排，同排用 | 分隔，每个写「显示文字」或「显示文字=点了替对方发的话」，网页写「显示文字=https://…」；" +
			"翻页、确认、投票等不必让群里看到的写「显示文字=回调:要交给你的话」，点了只悄悄通知你；" +
			"项前可加「主要:」「危险:」「次要:」，单独一行「小号」「大号」调字号；" +
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
//
// 模型写的一行是一组选项，但它不管字数，常把三四个长标签塞进一排；这里按标签
// 长度把放不下的那组拆成几排（见 fitButtonRow），同组仍然互斥。
func parseFinalizeButtons(value string) [][]OutgoingButton {
	var rows [][]OutgoingButton
	for lineIndex, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if len(rows) == qqKeyboardMaxRows {
			break
		}
		var row []OutgoingButton
		for _, item := range strings.Split(line, "|") {
			if len(row) == qqKeyboardMaxPerRow {
				break
			}
			item, style := cutButtonStylePrefix(strings.TrimSpace(item))
			label, data, hasData := strings.Cut(item, "=")
			label, data = strings.TrimSpace(label), strings.TrimSpace(data)
			callback := false
			if rest, ok := strings.CutPrefix(data, qqButtonCallbackPrefix); ok {
				callback, data = true, strings.TrimSpace(rest)
			}
			if !hasData || data == "" {
				data = label
			}
			if label == "" {
				continue
			}
			button := OutgoingButton{Label: cutButtonText(label, qqButtonLabelMaxRunes), Data: cutButtonText(data, qqButtonDataMaxRunes), Style: style, Group: lineIndex + 1}
			lower := strings.ToLower(button.Data)
			button.Link = strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
			button.Callback = callback && !button.Link
			row = append(row, button)
		}
		for _, fitted := range fitButtonRow(row) {
			if len(rows) == qqKeyboardMaxRows {
				break
			}
			rows = append(rows, fitted)
		}
	}
	return rows
}

// cutButtonStylePrefix 拿掉一项最前面的用途名，返回剩下的部分和对应样式。
func cutButtonStylePrefix(item string) (string, *int) {
	for prefix, style := range qqButtonStylePrefixes {
		if rest, ok := strings.CutPrefix(item, prefix); ok {
			return strings.TrimSpace(rest), &style
		}
	}
	return item, nil
}

// cutKeyboardFontSize 拿掉单独写字号的那行，返回剩下的按钮文本和字号。
func cutKeyboardFontSize(value string) (string, string) {
	fontSize := ""
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if size, ok := qqKeyboardFontSizes[strings.TrimSpace(line)]; ok {
			fontSize = size
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), fontSize
}

// qqButtonsStyled 判断这组按钮有没有用到默认以外的样式或字号。
func qqButtonsStyled(rows [][]OutgoingButton, fontSize string) bool {
	if fontSize != "" {
		return true
	}
	for _, row := range rows {
		for _, button := range row {
			if button.Style != nil && *button.Style != qqButtonStyleBlue {
				return true
			}
		}
	}
	return false
}

// plainQQButtons 复制一份去掉样式的按钮，样式被平台拒收时用它重发。
func plainQQButtons(rows [][]OutgoingButton) [][]OutgoingButton {
	out := make([][]OutgoingButton, len(rows))
	for index, row := range rows {
		out[index] = append([]OutgoingButton(nil), row...)
		for i := range out[index] {
			out[index][i].Style = nil
		}
	}
	return out
}

// fitButtonRow 把一组按钮按最长的标签分成几排：每排放尽量多的按钮，但要让最长的
// 那个也放得下，各排个数尽量均匀（5 个放不下时拆成 3+2，而不是 4+1）。
func fitButtonRow(row []OutgoingButton) [][]OutgoingButton {
	if len(row) == 0 {
		return nil
	}
	widest := 0
	for _, button := range row {
		widest = max(widest, buttonLabelWidth(button.Label))
	}
	perRow := len(row)
	for perRow > 1 && widest > (qqKeyboardRowWidth-perRow*qqButtonPaddingWidth)/perRow {
		perRow--
	}
	count := (len(row) + perRow - 1) / perRow
	out := make([][]OutgoingButton, 0, count)
	for start := 0; count > 0; count-- {
		size := (len(row) - start + count - 1) / count
		out = append(out, row[start:start+size])
		start += size
	}
	return out
}

// buttonLabelWidth 按半角宽度计标签长度：汉字、全角符号和 emoji 算 2。
func buttonLabelWidth(label string) int {
	width := 0
	for _, r := range label {
		if r < 0x1100 || unicode.Is(unicode.Mn, r) {
			width++
			continue
		}
		width += 2
	}
	return width
}

// cutButtonText 按字数截断按钮文字，不加省略号：按钮放不下多余的字。
func cutButtonText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// qqOfficialKeyboard 把按钮行转成开放平台的自定义 keyboard 结构，fontSize 非空时写进
// content.style.font_size。
//
// 同一组有两个以上指令按钮时，它们共用一个 group_id：群友实测同组按钮互斥，点了
// 一个其余就灰掉，正好是「选一个」的语义；链接按钮不参与。官方文档没写这个字段。
// 一组因为太长被拆成几排时，几排仍是同一组。
func qqOfficialKeyboard(rows [][]OutgoingButton, fontSize string) map[string]any {
	group := func(rowIndex int, button OutgoingButton) string {
		if button.Group > 0 {
			return fmt.Sprintf("row%d", button.Group)
		}
		return fmt.Sprintf("row%d", rowIndex+1)
	}
	commands := map[string]int{}
	for rowIndex, row := range rows {
		for _, button := range row {
			if !button.Link {
				commands[group(rowIndex, button)]++
			}
		}
	}
	payloadRows := make([]any, 0, len(rows))
	for rowIndex, row := range rows {
		buttons := make([]any, 0, len(row))
		for index, button := range row {
			action := map[string]any{
				"type":           qqButtonActionCommand,
				"data":           button.Data,
				"enter":          true,
				"unsupport_tips": qqButtonUnsupportedTip,
				"permission":     map[string]any{"type": qqButtonPermissionAll},
			}
			if button.Callback {
				action["type"] = qqButtonActionCallback
				delete(action, "enter")
			}
			if button.Link {
				action = map[string]any{
					"type":           qqButtonActionLink,
					"data":           button.Data,
					"unsupport_tips": qqButtonUnsupportedTip,
					"permission":     map[string]any{"type": qqButtonPermissionAll},
				}
			}
			style := qqButtonStyleBlue
			if button.Style != nil {
				style = *button.Style
			}
			payload := map[string]any{
				"id":          fmt.Sprintf("%d-%d", rowIndex+1, index+1),
				"render_data": map[string]any{"label": button.Label, "visited_label": button.Label, "style": style},
				"action":      action,
			}
			if id := group(rowIndex, button); !button.Link && commands[id] > 1 {
				payload["group_id"] = id
			}
			buttons = append(buttons, payload)
		}
		payloadRows = append(payloadRows, map[string]any{"buttons": buttons})
	}
	content := map[string]any{"rows": payloadRows}
	if fontSize != "" {
		content["style"] = map[string]any{"font_size": fontSize}
	}
	return map[string]any{"content": content}
}

// qqOfficialKeyboardPayload 在 qqOfficialKeyboard 外面补上 bot_appid：社区插件用的
// qq-official-bot SDK 发自定义键盘都带它，文档外的样式是照着它们实测的，请求体跟它们对齐。
func (c *QQOfficialChannel) qqOfficialKeyboardPayload(rows [][]OutgoingButton, fontSize string) map[string]any {
	keyboard := qqOfficialKeyboard(rows, fontSize)
	if appID := strings.TrimSpace(c.cfg.AppID); appID != "" {
		keyboard["bot_appid"] = appID
	}
	return keyboard
}

type finalizeButtonsKey struct{}

// finalizeButtons 把收尾时填的按钮从 generateReply 带到发送阶段，挂在这一轮最后
// 一条文字消息上。只有主回复路径会在 ctx 里放它。
type finalizeButtons struct {
	mu       sync.Mutex
	rows     [][]OutgoingButton
	fontSize string
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
	value, fontSize := cutKeyboardFontSize(value)
	f.mu.Lock()
	f.rows, f.fontSize = parseFinalizeButtons(value), fontSize
	f.mu.Unlock()
}

// take 取出按钮和整组字号并清空，同一轮只挂一次。没有按钮时字号也不挂。
func (f *finalizeButtons) take() ([][]OutgoingButton, string) {
	if f == nil {
		return nil, ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, fontSize := f.rows, f.fontSize
	f.rows, f.fontSize = nil, ""
	if len(rows) == 0 {
		fontSize = ""
	}
	return rows, fontSize
}
