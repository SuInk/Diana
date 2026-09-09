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
	cfg := r.Config()
	admin := r.agentRegistryConfig(cfg, MessageEvent{Platform: PlatformOneBotV11}, true)
	admin, err := agent.GlobalExtensionPaths(admin)
	if err != nil {
		return nil, err
	}
	result, err := agent.AdministerExtensions(ctx, admin, req)
	if err == nil && (req.Operation == "save" || req.Operation == "delete") {
		r.closeAgentRegistryCache()
	}
	return result, err
}
