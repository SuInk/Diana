// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 工作区容器里的 run_command，照雨夹雪：主人、管理员、群成员用同一个工具、同一个容器，
// 隔离全交给容器（见 agent/workbox.go）。要动宿主机的事走 host_access。

const (
	boxCommandToolName = "run_command"
	boxResetToolName   = "box_reset"
)

// boxAvailable 判断这台机器能不能起 work box。测试里替换它，免得结果取决于跑测试的
// 机器有没有 Docker。
var boxAvailable = func() bool {
	ok, _ := agent.BoxStatus()
	return ok
}

// 工作区容器的三档开关。
const (
	BoxAccessOff   = "off"
	BoxAccessOwner = "owner"
	BoxAccessAll   = "all"
)

// DefaultBoxDiskLimitMB 是每个会话 /work 卷的默认上限，minBoxDiskLimitMB 是能设的最小值：
// 镜像里 pip/npm 缓存和一个中等仓库就要上百 MB，再小就什么都干不了。
const (
	DefaultBoxDiskLimitMB = agent.DefaultBoxDiskLimitBytes >> 20
	minBoxDiskLimitMB     = 100
)

// NormalizeBoxAccess 把配置值归一到三档；留空和不认识的值按「仅主人」，升级后主人
// 原来能用的 run_command 继续可用。
func NormalizeBoxAccess(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case BoxAccessOff:
		return BoxAccessOff
	case BoxAccessAll:
		return BoxAccessAll
	default:
		return BoxAccessOwner
	}
}

// boxExposed 决定这一轮挂不挂 run_command / box_reset：档位放行这位发言者、不在安全模式、
// box 可用。
func (r *Runtime) boxExposed(event MessageEvent, owner bool) bool {
	if r == nil {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	switch NormalizeBoxAccess(cfg.AgentBoxAccess) {
	case BoxAccessAll:
	case BoxAccessOwner:
		if !owner {
			return false
		}
	default:
		return false
	}
	return !cfg.agentSafeMode() && boxAvailable()
}

// boxKey 决定这一轮用哪个 box：每个机器人的每个会话（群、私聊）各一个。
func (r *Runtime) boxKey(event MessageEvent) string {
	cfg := r.effectiveConfigForEvent(event)
	key, _ := json.Marshal([]string{firstNonEmpty(event.ProfileID, cfg.ID, cfg.BotAccount), r.currentPlatform(event), sessionKey(event)})
	return string(key)
}

func (r *Runtime) boxForEvent(event MessageEvent, owner bool) (*agent.WorkBox, error) {
	if !r.boxExposed(event, owner) {
		return nil, errors.New("工作区容器没有打开，或这台机器上 Docker 不可用")
	}
	workBox, err := workBoxFor(r.boxKey(event))
	if err != nil {
		return nil, err
	}
	workBox.SetDiskLimit(int64(r.effectiveConfigForEvent(event).WithDefaults().AgentBoxDiskLimitMB) << 20)
	return workBox, nil
}

type boxCommandTool struct {
	runtime *Runtime
	event   MessageEvent
	owner   bool
}

func (t *boxCommandTool) Name() string        { return boxCommandToolName }
func (t *boxCommandTool) Description() string { return agent.BoxCommandDescription }

func (t *boxCommandTool) InputSchema() map[string]any { return agent.BoxCommandSchema() }

func (t *boxCommandTool) Run(ctx context.Context, input map[string]any) (string, error) {
	workBox, err := t.runtime.boxForEvent(t.event, t.owner)
	if err != nil {
		return "", err
	}
	return agent.RunBoxCommand(ctx, workBox, configToolString(input, "command"), time.Duration(intFromAny(input["timeout"]))*time.Second)
}

type boxResetTool struct {
	runtime *Runtime
	event   MessageEvent
	owner   bool
}

func (t *boxResetTool) Name() string { return boxResetToolName }
func (t *boxResetTool) Description() string {
	return "被要求或工作区坏掉时清空本会话的工作区（停掉本会话的命令、删光本会话目录里的文件）。别的会话不受影响。"
}
func (t *boxResetTool) InputSchema() map[string]any { return toolObjectSchema(nil, map[string]any{}) }

func (t *boxResetTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	workBox, err := t.runtime.boxForEvent(t.event, t.owner)
	if err != nil {
		return "", err
	}
	if err := workBox.Reset(ctx); err != nil {
		return "", err
	}
	return `{"reset":true}`, nil
}

// resetSessionBox 清空本会话的 box。Docker 不可用时没有 box 可清，直接返回。
func (r *Runtime) resetSessionBox(event MessageEvent) error {
	if !boxAvailable() {
		return nil
	}
	workBox, err := workBoxFor(r.boxKey(event))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return workBox.Reset(ctx)
}

// workBoxFor 返回 key 对应的会话范围。范围 uid 登记在工作目录的 .diana/ 里，第一次
// 用之前先把位置告诉 agent 包。
func workBoxFor(key string) (*agent.WorkBox, error) {
	agent.SetBoxStateDir(agent.DianaStateDir(AgentWorkspaceDir()))
	return agent.BoxFor(key)
}

// runBoxMaintenance 在有机器人打开工作区容器时预构建镜像；每分钟停掉空闲 15 分钟的共享
// 容器，并把旧版每会话一个的容器和卷并进共享工作区（搬完就不再检查）。
// Docker 不可用时这些都直接返回。
func runBoxMaintenance(ctx context.Context, configs []BotConfig) {
	enabled := false
	for _, cfg := range configs {
		cfg = cfg.WithDefaults()
		enabled = enabled || (cfg.Enabled && cfg.AgentEnabled && NormalizeBoxAccess(cfg.AgentBoxAccess) != BoxAccessOff && !cfg.agentSafeMode())
	}
	if enabled && boxAvailable() {
		if err := agent.PrewarmBoxImage(ctx); err != nil {
			log.Printf("diana agent: 工作区镜像预构建失败：%v", err)
		}
	}
	// 回收和迁移不看开关：开关后来才打开、或者关掉之前留下的容器，也按时处理。
	agent.SetBoxStateDir(agent.DianaStateDir(AgentWorkspaceDir()))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	migrating := true
	for {
		now := time.Now()
		if migrating && boxAvailable() {
			migrating = agent.MigrateLegacyBoxes(ctx)
		}
		agent.StopIdleBoxes(ctx, now, agent.BoxIdleTimeout)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
