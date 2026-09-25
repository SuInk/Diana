// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// keepIndexPromptLimit 是回复提示词里最多列出的长期区文件数，按保存时间取最新的。
const keepIndexPromptLimit = 20

// 长期保存区的清单常驻在回复提示词里：主人上周说「把这张海报留着」，这周问「上次那张
// 海报还在吗」，模型不该先去 list_files 翻目录再猜哪个文件是。清单只有路径和一句说明，
// 二十条以内，超出的只报个数。
//
// 它随机器人、随长期区内容变，所以进尾部；同一份索引渲染出来逐字相同，长期区没变时
// 不会让尾部前面的缓存作废。只对主人、且文件工具可用时注入：普通成员拿不到文件工具，
// 看见清单也用不上，还白白知道了主人存了什么。
var promptWorkspaceKeepIndexSpec = registerPrompt(PromptSpec{
	Key:   "reply.tool.workspace_keep_index",
	Group: PromptGroupReplyTools,
	Title: "长期保存区清单",
	Usage: "当前发言者是主人、文件工具可用、且这台机器人的长期保存区 keep/ 里有文件时放在尾部：列出最近保存的文件和说明，主人问起存过的东西时先看这里。",
	Default: "这台机器人的长期保存区是 {keep_dir}/（不会自动清理，删除用 manage_files delete 进回收站），现有 {count} 个文件，最近保存的：\n{entries}\n" +
		"主人问起以前存过的东西时先对照这份清单，用 read_file、view_image 或 send_attachment 按路径取用；清单里没有的再用 list_files {keep_dir}/ 查。",
	Vars: []PromptVar{
		{Name: "keep_dir", Description: "这台机器人长期区的相对路径，形如 keep/<机器人>"},
		{Name: "count", Description: "长期区里登记过、文件仍在的条目数"},
		{Name: "entries", Description: "最近保存的至多 20 条，每行「路径  说明」，超出时末尾注明还有几条"},
	},
})

// keepIndexPrompt 渲染这台机器人长期区的清单；长期区为空或读不到索引时返回空串，
// 不注入任何东西。
func keepIndexPrompt(cfg BotConfig, botID string) string {
	area := agent.KeepAreaPath(botID)
	if area == "" {
		return ""
	}
	root := AgentWorkspaceDir()
	entries, err := agent.LoadKeepIndex(root, botID)
	if err != nil || len(entries) == 0 {
		return ""
	}
	// 索引可能落后于磁盘：文件被 WebUI 或命令删掉时索引没跟上。只列还在的，免得模型
	// 拿着一条已经不存在的路径去发文件。
	present := entries[:0:0]
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(entry.Path))); err == nil {
			present = append(present, entry)
		}
	}
	if len(present) == 0 {
		return ""
	}
	lines := make([]string, 0, keepIndexPromptLimit+1)
	for i, entry := range present {
		if i >= keepIndexPromptLimit {
			lines = append(lines, fmt.Sprintf("- ……另有 %d 个较早的，用 list_files %s/ 查看", len(present)-keepIndexPromptLimit, area))
			break
		}
		line := "- " + entry.Path
		if entry.IsDir {
			line += "/"
		}
		if description := strings.TrimSpace(entry.Description); description != "" {
			line += "  " + description
		}
		lines = append(lines, line)
	}
	return cfg.promptf(promptWorkspaceKeepIndexSpec, map[string]string{
		"keep_dir": area,
		"count":    strconv.Itoa(len(present)),
		"entries":  strings.Join(lines, "\n"),
	})
}
