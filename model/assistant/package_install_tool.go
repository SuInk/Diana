// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

const dianaInstallPackageToolName = "install_package"

// dianaInstallPackageTool 让主人的机器人缺包时自己装上，不用先停下来问。
// 不确认但不悄悄装：动手前先往会话里发一条提醒，装了什么主人都看得见。
// 只装进工作区的 node_modules，不收任意来源，所以和编码代理一样只挂给主人、
// 安全模式下不构造。
type dianaInstallPackageTool struct {
	runtime *Runtime
	event   MessageEvent
	root    string
	mcp     string
	// install 和 notify 留给测试替换。
	install func(ctx context.Context, root, mcpConfigPath string, packages []string) (string, error)
	notify  func(ctx context.Context, text string) bool
}

func newDianaInstallPackageTool(runtime *Runtime, event MessageEvent, cfg BotConfig) *dianaInstallPackageTool {
	tool := &dianaInstallPackageTool{runtime: runtime, event: event, root: AgentWorkspaceDir(), mcp: cfg.AgentMCPConfigPath, install: agent.InstallNPMPackages}
	tool.notify = tool.announce
	return tool
}

func (t *dianaInstallPackageTool) Name() string { return dianaInstallPackageToolName }

func (t *dianaInstallPackageTool) Description() string {
	return `缺 npm 包时直接装进 Agent 工作区的 node_modules，装好后工作区里的 Node.js 脚本可直接 require/import，仅主人可用。会先在会话里发一条提醒再安装，不需要等主人确认。只收 npm 注册表包名（可带 @版本），不支持 URL、git 地址或全局安装；运行脚本仍用 run_command。`
}

func (t *dianaInstallPackageTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"packages"}, map[string]any{
		"packages": toolStringArrayParam("要安装的 npm 包，如 sharp、lodash@4；最多 " + itoa(agent.MaxInstallPackages) + " 个"),
		"reason":   toolStringParam("一句话说明为什么要装，会写进提醒里"),
	})
}

func (t *dianaInstallPackageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	values, err := toolStringValues(input["packages"])
	if err != nil {
		return "", err
	}
	packages, err := agent.ValidateNPMPackages(values)
	if err != nil {
		return "", err
	}
	notice := "📦 正在安装 npm 包：" + strings.Join(packages, "、")
	if reason := strings.TrimSpace(inputString(input, "reason")); reason != "" {
		notice += "\n用途：" + truncateRunes(reason, 80)
	}
	notice += "\n装到工作区 node_modules，不影响系统环境。"
	notified := t.notify(ctx, notice)
	output, err := t.install(ctx, t.root, t.mcp, packages)
	result := map[string]any{
		"packages":     packages,
		"installed_to": agent.NPMModulesDir(t.root),
		"notified":     notified,
	}
	if output != "" {
		result["output"] = output
	}
	if err != nil {
		result["error"] = err.Error()
	} else {
		result["status"] = "installed"
		result["next"] = "用 run_command 运行工作区里的 node 脚本，require 可直接找到这些包"
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil {
		return string(body), err
	}
	return string(body), nil
}

func (t *dianaInstallPackageTool) announce(ctx context.Context, text string) bool {
	if t.runtime == nil {
		return false
	}
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{Text: text})); err != nil {
		log.Printf("diana install notice not sent: %v", err)
		return false
	}
	// 话已经出去了，这一轮不能再被合并重来，否则提醒会重复发。
	t.runtime.sealDirectReply(ctx)
	if typing := typingIndicatorFromContext(ctx); typing != nil {
		typing.resume()
	}
	return true
}
