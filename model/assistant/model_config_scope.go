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
	profile, ok := r.lookupProfileLocked("")
	if !ok {
		return BotConfig{}, fmt.Errorf("多机器人模式下缺少消息所属机器人，未修改配置")
	}
	return profile.WithDefaults(), nil
}

type adminChatModelContextKey struct{}

// WithAdminChatModel pins the admin chat run to one provider model without
// touching the bot's own model roles. Empty values keep the bot's routing.
func WithAdminChatModel(ctx context.Context, providerID, model string) context.Context {
	providerID, model = strings.TrimSpace(providerID), strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return ctx
	}
	return context.WithValue(ctx, adminChatModelContextKey{}, ModelRole{ProfileID: providerID, Model: model})
}

func (r *Runtime) modelRolesForContext(ctx context.Context) map[string]ModelRole {
	roles := r.baseModelRolesForContext(ctx)
	if ctx == nil {
		return roles
	}
	if override, ok := ctx.Value(adminChatModelContextKey{}).(ModelRole); ok {
		pinned := make(map[string]ModelRole, len(roles)+1)
		for key, role := range roles {
			pinned[key] = role
		}
		pinned["admin_chat"] = override
		return pinned
	}
	return roles
}

func (r *Runtime) baseModelRolesForContext(ctx context.Context) map[string]ModelRole {
	if ctx != nil {
		if id, ok := ctx.Value(modelProfileContextKey{}).(string); ok && id != "" {
			return normalizeModelRoles(r.effectiveConfigForEvent(MessageEvent{ProfileID: id}).ModelRoles)
		}
	}
	if usage := llmUsageFromContext(ctx); usage != nil && usage.event.ProfileID != "" {
		return normalizeModelRoles(r.effectiveConfigForEvent(usage.event).ModelRoles)
	}
	return normalizeModelRoles(r.profileConfig("").ModelRoles)
}
