// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 工作区容器里的 run_command，照雨夹雪：主人、管理员、群成员用同一个工具、同一个容器，
// 隔离全交给容器（见 agent/workbox.go）。要动宿主机的事走 host_access。

const (
	boxCommandToolName = "run_command"
	boxResetToolName   = "box_reset"
	boxMaxInputs       = 10
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
	workBox, err := agent.BoxFor(r.boxKey(event))
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

func (t *boxCommandTool) InputSchema() map[string]any {
	schema := agent.BoxCommandSchema()
	schema["properties"].(map[string]any)["inputs"] = toolStringArrayParam(fmt.Sprintf("运行前放进 /work/inbox 的工作目录文件（如 save_to_workspace 存下的聊天附件），最多 %d 个", boxMaxInputs))
	return schema
}

func (t *boxCommandTool) Run(ctx context.Context, input map[string]any) (string, error) {
	workBox, err := t.runtime.boxForEvent(t.event, t.owner)
	if err != nil {
		return "", err
	}
	staged, err := t.stageInputs(ctx, workBox, configToolStringSlice(input, "inputs"))
	if err != nil {
		return "", err
	}
	out, err := agent.RunBoxCommand(ctx, workBox, configToolString(input, "command"), time.Duration(intFromAny(input["timeout"]))*time.Second)
	if len(staged) > 0 && err == nil {
		out = fmt.Sprintf("inputs 已放进：%s\n%s", strings.Join(staged, "、"), out)
	}
	return out, err
}

// stageInputs 把工作目录文件放进 /work/inbox。群成员只能传本群保存过的文件（核对访问
// 哈希），主人不受这条限制，受保护的运行时配置谁都传不进去。
func (t *boxCommandTool) stageInputs(ctx context.Context, workBox *agent.WorkBox, paths []string) ([]string, error) {
	if len(paths) > boxMaxInputs {
		return nil, fmt.Errorf("inputs 一次最多 %d 个文件", boxMaxInputs)
	}
	cfg := agent.Config{WorkDir: AgentWorkspaceDir(), MCPConfigPath: t.runtime.effectiveConfigForEvent(t.event).AgentMCPConfigPath}
	var staged []string
	for _, raw := range paths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		rel, err := agent.NormalizeWorkspacePath(cfg.WorkDir, raw)
		if err != nil {
			return nil, err
		}
		if agent.WorkspaceFileProtected(cfg, rel) {
			return nil, fmt.Errorf("%s 是受保护的运行时配置，不能放进工作区容器", raw)
		}
		var grant string
		if !t.owner {
			if grant, err = t.runtime.workspaceGroupFileGrant(t.event, rel); err != nil {
				return nil, err
			}
		}
		data, err := agent.ReadWorkspaceFile(cfg.WorkDir, rel, agent.BoxCopyOutMaxBytes)
		if err != nil {
			return nil, err
		}
		if !t.owner && workspaceAccessHash(data) != grant {
			return nil, fmt.Errorf("文件 %s 内容已改变，本群原有访问记录不再适用；请从本群原消息重新保存", raw)
		}
		target, err := workBox.WriteFile(ctx, path.Join("inbox", filepath.Base(rel)), bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		staged = append(staged, target)
	}
	return staged, nil
}

type boxResetTool struct {
	runtime *Runtime
	event   MessageEvent
	owner   bool
}

func (t *boxResetTool) Name() string { return boxResetToolName }
func (t *boxResetTool) Description() string {
	return "被要求或容器坏掉时清空工作区容器（/work 全部删除）。每周一 05:00 也会自动清空。"
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
	workBox, err := agent.BoxFor(r.boxKey(event))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return workBox.Reset(ctx)
}

// boxFileSource 是 send_file 等工具从 box 取文件：路径要以 /work 开头。
func (r *Runtime) boxFileSource(ctx context.Context, event MessageEvent, owner bool, p string, limit int64) ([]byte, error) {
	workBox, err := r.boxForEvent(event, owner)
	if err != nil {
		return nil, err
	}
	_, data, err := workBox.CopyOut(ctx, p, min(limit, agent.BoxCopyOutMaxBytes))
	return data, err
}

// runBoxMaintenance 在有机器人打开工作区容器时预构建镜像；每分钟停掉空闲 15 分钟的容器，
// 每 10 分钟检查一次每周重置。
// Docker 不可用时 RunWeeklyBoxReset 直接返回。
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
	// 回收和每周重置不看开关：开关后来才打开、或者关掉之前留下的容器，也按时处理。
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastReset := time.Time{}
	for {
		now := time.Now()
		agent.StopIdleBoxes(ctx, now, agent.BoxIdleTimeout)
		if now.Sub(lastReset) >= agent.BoxResetCheckInterval {
			agent.RunWeeklyBoxReset(ctx, now)
			lastReset = now
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func isBoxPath(p string) bool {
	return p == agent.BoxWorkDir || strings.HasPrefix(p, agent.BoxWorkDir+"/")
}
