package assistant

import (
	"context"

	"github.com/SuInk/diana/model/agent"
)

// 主回复的前缀按本群的工具全集渲染（见 agent.Config.HeadRegistry）：非主人说话时另建
// 一份主人口径的注册表，只用来渲染工具目录、规则和契约，从不执行。

type headAgentRegistryKey struct{}

// withHeadAgentRegistry 把渲染用的工具全集交给这一轮后面的 generateReply。
func withHeadAgentRegistry(ctx context.Context, head *agent.ToolRegistry) context.Context {
	if head == nil {
		return ctx
	}
	return context.WithValue(ctx, headAgentRegistryKey{}, head)
}

// headAgentRegistryFrom 取这一轮渲染用的工具全集；没有时就是执行用的 registry。
func headAgentRegistryFrom(ctx context.Context, registry *agent.ToolRegistry) *agent.ToolRegistry {
	if head, ok := ctx.Value(headAgentRegistryKey{}).(*agent.ToolRegistry); ok && head != nil {
		return head
	}
	return registry
}

// ownerToolScope 是渲染工具全集用的主人口径：拿到主人那套工具，但不是管理员（主人本人
// 才有的增减管理员也列出来）。
func ownerToolScope(relationship RelationshipPolicy) RelationshipPolicy {
	relationship.Owner = true
	relationship.Admin = false
	return relationship
}

// ownerRenderEvent 是建渲染用注册表时用的事件：发送者换成主人，github 这类按「是不是
// 主人在问」写描述的工具，描述和主人本人说话时逐字一样。这份注册表只渲染、从不执行。
func ownerRenderEvent(cfg BotConfig, event MessageEvent) MessageEvent {
	if owner := cfg.OwnerIDForEvent(event); owner != "" {
		event.UserID = owner
	}
	return event
}

// headRegistryForPrompt 是系统提示词按哪份工具全集写规则：只有 Agent 在跑、而且另建了
// 全集时才按全集写，否则照旧按发言者分头部和尾部。
func headRegistryForPrompt(agentActive bool, registry, head *agent.ToolRegistry) *agent.ToolRegistry {
	if !agentActive || head == nil {
		return nil
	}
	return head
}

// memoryToolAvailable 判断这一轮模型能不能自己用 memory 工具查长期记忆。
func memoryToolAvailable(agentActive bool, registry *agent.ToolRegistry) bool {
	if !agentActive || registry == nil {
		return false
	}
	_, ok := registry.Get(dianaMemoryToolName)
	return ok
}
