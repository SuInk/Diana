package assistant

import (
	"context"
	"fmt"
	"strings"
)

type ModelRoleConfigSaver interface {
	SaveModelRole(BotConfig, string, ModelRole) (BotConfig, error)
}

type modelProfileContextKey struct{}

func withModelConfigEvent(ctx context.Context, event MessageEvent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id := strings.TrimSpace(event.ProfileID); id != "" {
		return context.WithValue(ctx, modelProfileContextKey{}, id)
	}
	return ctx
}

func (r *Runtime) modelConfigForEvent(event MessageEvent) (BotConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id := strings.TrimSpace(event.ProfileID); id != "" {
		if cfg, ok := r.profileConfigs[id]; ok {
			return cfg.WithDefaults(), nil
		}
		return BotConfig{}, fmt.Errorf("消息所属机器人 %q 不存在", id)
	}
	if len(r.profileConfigs) > 1 {
		return BotConfig{}, fmt.Errorf("多机器人模式下缺少消息所属机器人，未修改配置")
	}
	return r.cfg.WithDefaults(), nil
}

func (r *Runtime) modelRolesForContext(ctx context.Context) map[string]ModelRole {
	if ctx != nil {
		if id, ok := ctx.Value(modelProfileContextKey{}).(string); ok && id != "" {
			return normalizeModelRoles(r.effectiveConfigForEvent(MessageEvent{ProfileID: id}).ModelRoles)
		}
	}
	if usage := llmUsageFromContext(ctx); usage != nil && usage.event.ProfileID != "" {
		return normalizeModelRoles(r.effectiveConfigForEvent(usage.event).ModelRoles)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return normalizeModelRoles(r.cfg.ModelRoles)
}
