// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 跟评可以先查再说：Release 只有版本号、PR 只有标题时，模型能去读更新说明、翻 PR、
// 搜一下背景，而不是围着推送正文里那一行打转。
//
// 只给只读工具。推送正文和参考资料都来自外部仓库，里面可能夹着注入指令；仓库订阅
// 跟评又是定时触发、没有发言人。所以按「普通群友」装配工具（不按主人），再裁成下面
// 这张白名单，GitHub 的写操作单独关掉。跟评要说的话仍走 sendFollowUp 发出去，
// 工具这一路不能发消息。

// followUpReadOnlyTools 是跟评能用的工具。都只读：搜索和沙盒渲染读公网，chat_history
// 读本会话历史，github 只留读操作，version 读本机版本。
var followUpReadOnlyTools = []string{
	agent.WebSearchToolName,
	sandboxedBrowserToolName,
	dianaChatHistoryToolName,
	dianaGitHubToolName,
	dianaVersionToolName,
}

// followUpMaxToolSteps 限制跟评的工具轮数：它是推送后顺口一句，不是一次调研。
const followUpMaxToolSteps = 4

// followUpReadOnlyRegistry 装配跟评用的只读工具；Agent 没开或一个都装不上时返回 nil，
// 调用方退回不带工具的单次生成。
func (r *Runtime) followUpReadOnlyRegistry(ctx context.Context, cfg BotConfig, source MessageEvent) *agent.ToolRegistry {
	if !cfg.AgentEnabled {
		return nil
	}
	registry, err := r.newReplyAgentRegistry(ctx, cfg, source, RelationshipPolicy{}, nil)
	if err != nil {
		log.Printf("diana follow-up tools unavailable: %v", err)
		return nil
	}
	if registry == nil {
		return nil
	}
	restrictFollowUpRegistry(registry)
	if len(followUpToolNames(registry)) == 0 {
		_ = registry.Close()
		return nil
	}
	return registry
}

// restrictFollowUpRegistry 把注册表裁成只读白名单，并关掉 GitHub 的写操作。
func restrictFollowUpRegistry(registry *agent.ToolRegistry) {
	allowed := make(map[string]bool, len(followUpReadOnlyTools))
	for _, name := range followUpReadOnlyTools {
		allowed[name] = true
	}
	registry.Retain(allowed)
	if operations := githubWriteOperations(); len(operations) > 0 {
		registry.DisableOperations("跟评只能读，不能写仓库", agent.DisabledOperation{Tool: dianaGitHubToolName, Field: "operation", Values: operations})
	}
}

// githubWriteOperations 取安全模式里登记的 GitHub 写操作，两边用同一份清单。
func githubWriteOperations() []string {
	for _, rule := range AgentSafeModeRules {
		if rule.Tool == dianaGitHubToolName && len(rule.Operations) > 0 {
			return append([]string(nil), rule.Operations...)
		}
	}
	return nil
}

func followUpToolNames(registry *agent.ToolRegistry) []string {
	if registry == nil {
		return nil
	}
	var names []string
	for _, name := range followUpReadOnlyTools {
		if _, ok := registry.Get(name); ok {
			names = append(names, name)
		}
	}
	return names
}

// generateFollowUpText 有只读工具时走 Agent 多步，否则单次生成。registry 由这里关闭。
func (r *Runtime) generateFollowUpText(ctx context.Context, cfg BotConfig, messages []llm.Message, registry *agent.ToolRegistry) (string, error) {
	if registry != nil {
		agentCfg := agent.Config{
			WorkDir:                     AgentWorkspaceDir(),
			MaxSteps:                    followUpMaxToolSteps,
			CoreTools:                   followUpToolNames(registry),
			BrowserTimeoutMS:            cfg.AgentBrowserTimeoutMS,
			BrowserScreenshotDisabled:   true,
			BrowserOperationDisabled:    true,
			BrowserScreenshotRestricted: true,
		}
		runner, err := agent.NewRunner(newRuntimeAgentLLMProvider(r, ctx), agentCfg, registry)
		if err != nil {
			_ = registry.Close()
			return "", err
		}
		defer runner.Close()
		resp, err := runner.Run(ctx, agent.Request{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}
	return r.runLLMProviderForGroup(ctx, llm.GroupChat, func(client LLMProvider) (string, error) {
		llmResp, llmErr := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if llmErr != nil {
			return "", llmErr
		}
		return llmResp.Text, nil
	})
}
