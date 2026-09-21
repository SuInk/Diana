// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MCPPreset 是一条内置的 MCP 接入模板：告诉界面要问用户哪几个字段，以及怎么
// 把这些字段拼成一份 MCP 配置。
//
// 预设只是填表的模板，装上之后就是一条普通的 MCP 服务，改配置、停用、删除都走
// 原来那套。少数预设会随 Diana 一起打包上游的二进制（目前只有 gitea-mcp），这
// 等于把别人的发布链路接进我们的供应链，所以只收官方发布的产物，版本和 SHA-256
// 钉在 scripts/fetch-gitea-mcp.sh 里，对不上就构建失败。
type MCPPreset struct {
	ID string `json:"id"`
	// Name 是装上以后这条 MCP 的名字，用户可以改。
	Name       string               `json:"name"`
	Title      string               `json:"title"`
	Summary    string               `json:"summary"`
	DocsURL    string               `json:"docs_url,omitempty"`
	Transports []MCPPresetTransport `json:"transports"`
}

// MCPPresetTransport 是同一个服务的一种接法。远程和本地进程要问的东西不一样，
// 所以字段跟着接法走，而不是堆在一起让用户自己猜哪些该填。
type MCPPresetTransport struct {
	ID     string           `json:"id"`
	Label  string           `json:"label"`
	Hint   string           `json:"hint,omitempty"`
	Fields []MCPPresetField `json:"fields"`
	config func(map[string]string) map[string]any
}

type MCPPresetField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Hint        string `json:"hint,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Secret 的值不回显，和 MCP 的请求头、环境变量一样只写不读。
	Secret bool `json:"secret,omitempty"`
}

// mcpPresets 是内置清单。加一条服务只要往这里加，界面按字段自己渲染。
var mcpPresets = []MCPPreset{giteaMCPPreset()}

// giteaMCPPreset 接 Gitea 官方的 gitea-mcp：它同时支持 stdio 和 HTTP，实例地址
// 和访问令牌走 GITEA_HOST / GITEA_ACCESS_TOKEN，自建实例填自己的域名即可。
//
// 二进制随 Diana 一起发布，镜像和 Release 包里都有，所以默认那一项是「填地址和
// 令牌就能用」；已经自己跑了一份 gitea-mcp 的，仍然可以走 HTTP 接上去。
func giteaMCPPreset() MCPPreset {
	return MCPPreset{
		ID:      "gitea",
		Name:    "gitea",
		Title:   "Gitea",
		Summary: "接入自建或公有 Gitea 的仓库、Issue 与 Pull Request。官方 gitea-mcp 随 Diana 一起打包，填实例地址和访问令牌就能用。",
		DocsURL: "https://gitea.com/gitea/gitea-mcp",
		Transports: []MCPPresetTransport{
			{
				ID:    "stdio",
				Label: "用自带的 gitea-mcp",
				Hint:  "推荐：Diana 直接拉起随包发布的 gitea-mcp，令牌只存在这条 MCP 的环境变量里，不经过第三方。",
				Fields: []MCPPresetField{
					{Key: "host", Label: "Gitea 实例地址", Placeholder: "https://git.example.com", Required: true},
					{Key: "token", Label: "访问令牌", Hint: "Gitea 里生成的个人访问令牌，按 MCP 环境变量存放，不回显。", Required: true, Secret: true},
					{Key: "command", Label: "可执行文件", Placeholder: "留空用自带的那份", Hint: "只有要换成自己编译或另外安装的 gitea-mcp 时才填，可填命令名或绝对路径。"},
				},
				config: func(values map[string]string) map[string]any {
					command := strings.TrimSpace(values["command"])
					if command == "" {
						command = bundledGiteaMCPCommand()
					}
					return map[string]any{
						"command": command,
						"args":    []any{"-t", "stdio"},
						"env": map[string]any{
							"GITEA_HOST":         values["host"],
							"GITEA_ACCESS_TOKEN": values["token"],
						},
					}
				},
			},
			{
				ID:    "http",
				Label: "连接已经跑起来的 gitea-mcp",
				Hint:  "已经用官方 Docker 镜像或别的机器跑了一份 gitea-mcp（-t http）时填它的地址。令牌配在那一侧，Diana 不经手。",
				Fields: []MCPPresetField{
					{Key: "url", Label: "gitea-mcp 服务地址", Placeholder: "http://127.0.0.1:8080/mcp", Required: true},
					{Key: "authorization", Label: "Authorization 请求头", Placeholder: "留空表示不带", Hint: "只有给 gitea-mcp 另加了鉴权时才需要填。", Secret: true},
				},
				config: func(values map[string]string) map[string]any {
					cfg := map[string]any{"url": values["url"]}
					if token := strings.TrimSpace(values["authorization"]); token != "" {
						cfg["headers"] = map[string]any{"Authorization": token}
					}
					return cfg
				},
			},
		},
	}
}

// bundledGiteaMCPCommand 指向随 Diana 一起发布的那份 gitea-mcp。它就放在主程序
// 旁边，不在 PATH 里，所以必须给绝对路径。找不到就退回裸名字交给 PATH：宿主机
// 自己装过的照样能用，真的两头都没有时报的是「找不到命令」，比在这里提前失败好查。
func bundledGiteaMCPCommand() string {
	name := "gitea-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		return name
	}
	// 一键安装会在 PATH 里放软链，顺着链接找才能落到真正的安装目录。
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return bundledCommandIn(filepath.Dir(executable), name)
}

// bundledCommandIn 在指定目录里找这个命令，没有就退回裸名字交给 PATH。
func bundledCommandIn(dir, name string) string {
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return name
}

// MCPPresetList 返回内置清单，供界面渲染。
func MCPPresetList() []MCPPreset {
	out := make([]MCPPreset, len(mcpPresets))
	copy(out, mcpPresets)
	return out
}

// mcpPresetConfig 按预设和接法拼出一份 MCP 配置。必填项缺一个就直接说是哪一个，
// 不要等连接失败再让人回头猜。
func mcpPresetConfig(presetID, transportID string, values map[string]string) (map[string]any, error) {
	for _, preset := range mcpPresets {
		if preset.ID != presetID {
			continue
		}
		for _, transport := range preset.Transports {
			if transport.ID != transportID {
				continue
			}
			clean := map[string]string{}
			for _, field := range transport.Fields {
				value := strings.TrimSpace(values[field.Key])
				if field.Required && value == "" {
					return nil, fmt.Errorf("请填写「%s」", field.Label)
				}
				clean[field.Key] = value
			}
			return transport.config(clean), nil
		}
		return nil, fmt.Errorf("预设 %s 没有 %s 这种接法", presetID, transportID)
	}
	return nil, fmt.Errorf("预设不存在")
}
