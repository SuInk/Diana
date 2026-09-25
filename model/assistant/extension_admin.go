package assistant

import (
	"context"
	"fmt"

	"github.com/SuInk/diana/model/agent"
)

func (r *Runtime) AdministerExtensions(ctx context.Context, req agent.ExtensionAdminRequest) (any, error) {
	if req.ProfileID != "" {
		r.mu.RLock()
		_, ok := r.profileConfigs[req.ProfileID]
		r.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("机器人不存在")
		}
	}
	// 扩展路径是全局固定的（见 GlobalExtensionPaths），取哪台机器人的配置都指向同一处。
	cfg := r.profileConfig(req.ProfileID)
	admin := r.agentRegistryConfig(cfg, MessageEvent{ProfileID: cfg.ID, Platform: cfg.Platform}, true)
	// 常驻档位就地增删时，没列过名单的机器人要先把内置推荐名单固定下来再改。
	// agentRegistryConfig 不带 CoreTools，以前这里拿到的推荐名单是空的，第一次在
	// 扩展页点一下常驻，推荐的核心工具就全被清出了名单。聊天那条路一直用的是它。
	admin.CoreTools = replyAgentCoreTools
	admin, err := agent.GlobalExtensionPaths(admin)
	if err != nil {
		return nil, err
	}
	result, err := agent.AdministerExtensions(ctx, admin, req)
	if err == nil && agent.ExtensionRequestChangesDefinition(req) {
		r.closeAgentRegistryCache()
	}
	return result, err
}
