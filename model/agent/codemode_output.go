// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"os"
)

// codemode 结果给模型的上限照 Pi（pi-durable codemode/output.js）：默认 1 万 token，
// 按每 token 4 字折算；脚本可以用 max_output_tokens 调大。Pi 允许调到 4MB/4，靠到
// 阈值压缩兜底；Diana 的压缩只在空闲时做，一次结果撑爆 128K 窗口这一轮就失败，而且
// 会留在续接历史里，所以封顶 10 万字（约默认的 2.5 倍）。
// 超出时不报错、不丢掉已跑完的调用，保留开头和结尾各一半，完整输出存进工作区。
//
// 以前脚本输出超过 32KB 直接失败，没超的再按 ToolOutputMaxChars 只留开头 2 万字：
// 生产 2026-11-18 回放里「总结这周」按天读群聊的脚本撞上 32KB 失败，只好改成每天
// 只看中午一小段。
const (
	codeModeDefaultMaxOutputTokens = 10_000
	codeModeCharsPerToken          = 4
	codeModeMaxOutputChars         = 100_000
)

// codeModeOutputBudget 是这次脚本结果能给模型的字数。
func codeModeOutputBudget(input map[string]any) int {
	tokens := codeModeDefaultMaxOutputTokens
	switch value := input["max_output_tokens"].(type) {
	case float64:
		tokens = int(value)
	case int:
		tokens = value
	case int64:
		tokens = int(value)
	}
	if tokens <= 0 {
		tokens = codeModeDefaultMaxOutputTokens
	}
	return min(tokens*codeModeCharsPerToken, codeModeMaxOutputChars)
}

// codeModeModelOutput 把脚本结果装进 budget：装得下原样给，装不下给首尾各一半并写明
// 中间省略了多少、完整输出在哪。
func (r *Runner) codeModeModelOutput(output string, budget int) string {
	runes := []rune(output)
	if len(runes) <= budget {
		return output
	}
	head := budget / 2
	tail := budget - head
	removed := len(runes) - budget
	text := fmt.Sprintf("脚本输出共 %d 字，超出这次的上限 %d 字（max_output_tokens 默认 %d，可在 // @options 里调大），只给了开头 %d 字和结尾 %d 字，不要把这段当成全部输出。\n%s\n…中间省略 %d 字…\n%s",
		len(runes), budget, codeModeDefaultMaxOutputTokens, head, tail, string(runes[:head]), removed, string(runes[len(runes)-tail:]))
	if path, howToRead := r.spillCodeModeOutput(output); path != "" {
		return text + "\n完整输出已存到 " + path + "，" + howToRead + "。"
	}
	return text + "\n完整输出没有保存：要看中间部分，就让脚本只返回需要的字段，或调大 max_output_tokens 再跑。"
}

// spillCodeModeOutput 把完整输出存进 read_file 读得到的工作区；没有 read_file 时不存。
func (r *Runner) spillCodeModeOutput(output string) (string, string) {
	tool, ok := r.registry.Get("read_file")
	if !ok {
		return "", ""
	}
	reader, ok := tool.(*ReadFileTool)
	if !ok || reader.root == "" {
		return "", ""
	}
	file, err := os.CreateTemp("", "diana-codemode-output-*")
	if err != nil {
		return "", ""
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if _, err := file.WriteString(output); err != nil {
		return "", ""
	}
	path, howToRead, err := workspaceSpill(reader.root)(file)
	if err != nil {
		return "", ""
	}
	return path, howToRead
}
