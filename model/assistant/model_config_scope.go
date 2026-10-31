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
type modelEventContextKey struct{}

func withModelConfigEvent(ctx context.Context, event MessageEvent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, modelEventContextKey{}, MessageEvent{ProfileID: event.ProfileID, GroupID: event.GroupID, Kind: event.Kind})
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
		if event, ok := ctx.Value(modelEventContextKey{}).(MessageEvent); ok {
			return normalizeModelRoles(r.effectiveConfigForEvent(event).ModelRoles)
		}
		if id, ok := ctx.Value(modelProfileContextKey{}).(string); ok && id != "" {
			return normalizeModelRoles(r.effectiveConfigForEvent(MessageEvent{ProfileID: id}).ModelRoles)
		}
	}
	if usage := llmUsageFromContext(ctx); usage != nil && usage.event.ProfileID != "" {
		return normalizeModelRoles(r.effectiveConfigForEvent(usage.event).ModelRoles)
	}
	return normalizeModelRoles(r.profileConfig("").ModelRoles)
}

// ValidateGroupChatModel checks the saved provider catalog without making a paid request.
func (r *Runtime) ValidateGroupChatModel(model GroupChatModel) error {
	if model.Route != nil {
		role := *model.Route
		if role.FollowChat {
			return fmt.Errorf("群对话路由不能跟随自身；清空 chat_model 恢复机器人继承")
		}
		switch role.RoutingStrategy {
		case "", "smart", "failover", "round_robin", "weighted":
		default:
			return fmt.Errorf("调度策略无效")
		}
		for i, route := range append([]ModelRole{role}, role.Fallbacks...) {
			if i > 0 && len(route.Fallbacks) > 0 {
				return fmt.Errorf("后备路由不能嵌套")
			}
			if route.Group != "" {
				return fmt.Errorf("群独立路由请使用具体提供商和模型")
			}
			if err := r.ValidateGroupChatModel(GroupChatModel{ProfileID: route.ProfileID, Model: route.Model}); err != nil {
				return err
			}
		}
		return nil
	}

	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return fmt.Errorf("当前未接入提供商配置集")
	}
	for _, profile := range store.Profiles().WithDefaults().Profiles {
		if profile.ID != model.ProfileID {
			continue
		}
		if !profile.Config.ModelEnabled(model.Model) {
			return fmt.Errorf("模型 %q 已停用", model.Model)
		}
		if profile.Config.Model == model.Model {
			return nil
		}
		for _, item := range profile.Config.Models {
			if item.ID == model.Model {
				return nil
			}
		}
		return fmt.Errorf("提供商中没有模型 %q，请先同步模型列表", model.Model)
	}
	return fmt.Errorf("提供商 %q 不存在", model.ProfileID)
}

// SaveGroupChatModel updates only the model field so concurrent UI edits are retained.
type GroupChatModelSaver interface {
	SaveGroupChatModel(botProfileID, groupID string, model GroupChatModel, base BotConfig) error
}

func (r *Runtime) saveGroupChatModel(event MessageEvent, model GroupChatModel) error {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return fmt.Errorf("只能在群聊中修改当前群模型")
	}
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()
	cfg, err := r.modelConfigForEvent(event)
	if err != nil {
		return err
	}
	if !cfg.IsManagerEvent(event) || !boolValue(cfg.OwnerLLMConfigEnabled, true) {
		return fmt.Errorf("当前发送者无权修改此机器人的模型分配")
	}
	r.mu.RLock()
	store := r.groupConfigs
	r.mu.RUnlock()
	saver, ok := store.(GroupChatModelSaver)
	if !ok {
		return fmt.Errorf("当前部署未接入群模型配置存储")
	}
	return saver.SaveGroupChatModel(cfg.ID, strings.TrimSpace(event.GroupID), model, cfg)
}
