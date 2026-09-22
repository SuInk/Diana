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
	if err == nil && extensionWriteChangesRegistry(req.Operation) {
		r.closeAgentRegistryCache()
	}
	return result, err
}

// extensionWriteChangesRegistry 判断这次写入改没改扩展的定义本身。改了就得把缓存的
// 共享底座扔掉重建，否则新装的 MCP 要等到下次重启才出现在工具目录里——线上 09-22
// 就是这样：从预设装好瑞幸之后，模型在同一个进程里翻遍 capabilities、
// list_capabilities、tools_load、extension_access 也找不到它，8 格预算全花在找上。
//
// preset_save 在 agent 包内部才被改写成 save，这一侧看到的仍是 preset_save，所以
// 必须显式列出来。启用开关、成员档位、对象名单不在此列：它们每次请求都重新读，
// 不需要重建底座。
func extensionWriteChangesRegistry(operation string) bool {
	switch operation {
	case "save", "preset_save", "delete":
		return true
	default:
		return false
	}
}
