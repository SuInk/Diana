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
	admin, err := agent.GlobalExtensionPaths(admin)
	if err != nil {
		return nil, err
	}
	result, err := agent.AdministerExtensions(ctx, admin, req)
	if err == nil && agent.ExtensionOperationChangesDefinition(req.Operation) {
		r.closeAgentRegistryCache()
	}
	return result, err
}
