// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/runtimeconfig"
)

const dianaRuntimeConfigToolName = "runtime_config"

// SetRuntimeConfig 注入启动配置管理器，主人私聊里的 runtime_config 工具靠它查看和
// 改写 config.yaml。没注入时不挂这个工具。
func (r *Runtime) SetRuntimeConfig(manager *runtimeconfig.Manager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtimeConfig = manager
}

func (r *Runtime) runtimeConfigManager() *runtimeconfig.Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.runtimeConfig
}

// dianaRuntimeConfigTool 让主人在私聊里查看启动参数、环境变量和 config.yaml，
// 并改其中少数低风险的数值项。只挂在主人私聊：群里有旁人，查看结果里有路径
// 和部署信息。改动只写文件，重启才生效，工具不替主人重启。
type dianaRuntimeConfigTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaRuntimeConfigTool(runtime *Runtime, event MessageEvent) *dianaRuntimeConfigTool {
	return &dianaRuntimeConfigTool{runtime: runtime, event: event}
}

func (t *dianaRuntimeConfigTool) Name() string { return dianaRuntimeConfigToolName }

func (t *dianaRuntimeConfigTool) Description() string {
	return `查看 Diana 的启动配置：命令行参数、环境变量、config.yaml 基础设施项及各自来源，探测本机局域网/公网/回环地址和端口，改其中 bot_editable 的项。` +
		`仅在主人问到或明确要求改启动配置时调用；模型、接话等机器人设置不归这里。` +
		`update 前先向主人复述要改的项和新值，risky 项（端口、监听地址、数据库和媒体目录等）还要说清后果，主人明确同意后才带 confirmed=true 调用。改动写入 config.yaml，重启后生效，不会自动重启；改坏了可以再改回来。`
}

func (t *dianaRuntimeConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("get 查看配置，network 查本机局域网/公网/回环地址和监听端口，update 修改。", "get", "network", "update"),
		"public":    toolBoolParam("network 时是否同时探测公网出口地址（会访问外部服务）。"),
		"key":       toolStringParam("要改的配置项，如 storage.log_retention_days，取自 get。"),
		"value":     toolStringParam("新值；空字符串表示删掉这项、回到默认值。"),
		"confirmed": toolBoolParam("主人已明确同意这次修改。"),
	})
}

func (t *dianaRuntimeConfigTool) Run(ctx context.Context, input map[string]any) (string, error) {
	manager := t.runtime.runtimeConfigManager()
	if manager == nil {
		return "", errors.New("当前部署没有接入启动配置")
	}
	if t.event.Kind != EventKindPrivate || !t.runtime.effectiveConfigForEvent(t.event).IsOwnerEvent(t.event) {
		return "", errors.New("启动配置只能由主人在私聊里查看和修改")
	}
	switch t.CanonicalOperation(input) {
	case "get":
		snap, err := manager.Snapshot()
		if err != nil {
			return "", err
		}
		return marshalRuntimeConfigSnapshot(snap)
	case "network":
		snap, err := manager.Network(ctx, toolInputBool(input, "public"), false)
		if err != nil {
			return "", err
		}
		body, err := json.Marshal(snap)
		return string(body), err
	case "update":
	default:
		return "", errors.New("operation 必须是 get、network 或 update")
	}
	key := strings.TrimSpace(configToolString(input, "key"))
	field, ok := manager.Field(key)
	if !ok {
		return "", fmt.Errorf("未知配置项 %q，先用 get 查看", key)
	}
	if !field.BotEditable {
		return "", fmt.Errorf("%s 不能在聊天里改，请到控制台「设置 → 启动配置」修改", key)
	}
	if !toolInputBool(input, "confirmed") {
		return "", errors.New("修改前要先向主人复述配置项和新值，得到明确同意后带 confirmed=true 再调用")
	}
	value := configToolString(input, "value")
	snap, err := manager.Update(map[string]string{key: value}, runtimeconfig.ActorBot)
	if err != nil {
		return "", err
	}
	if writer := t.runtime.appLogWriter(); writer != nil {
		_ = writer.AppendLog(ctx, applog.Entry{
			Kind:    applog.KindOperation,
			Level:   applog.LevelInfo,
			Action:  "runtime_config_update",
			Message: "主人在私聊里修改了启动配置，重启后生效",
			Actor:   oneBotEventActor(t.event),
			Target:  key,
		})
	}
	for _, item := range snap.Items {
		if item.Key == key {
			body, err := json.Marshal(map[string]any{
				"ok": true, "key": key, "running_value": item.Value, "file_value": item.FileValue,
				"restart_needed": item.Pending, "config_path": snap.ConfigPath,
				"message": "已写入配置文件，重启服务后生效",
				"risky":   field.Risky,
				"warning": runtimeConfigRiskWarning(field),
			})
			return string(body), err
		}
	}
	return `{"ok":true}`, nil
}

// runtimeConfigRiskWarning 给 risky 项一句要转告主人的提醒，非 risky 返回空。
func runtimeConfigRiskWarning(field runtimeconfig.Field) string {
	switch {
	case !field.Risky:
		return ""
	case strings.HasPrefix(field.Key, "server."):
		return "重启后控制台的访问地址会变；Docker 要同步端口映射。连不上时改回原值或直接编辑配置文件。"
	default:
		return "不会自动搬旧数据，新位置没有数据时重启后是空的；改回原值重启即可恢复。"
	}
}

// marshalRuntimeConfigSnapshot 压掉前端才用得上的字段，只给模型留回答需要的部分。
func marshalRuntimeConfigSnapshot(snap runtimeconfig.Snapshot) (string, error) {
	type item struct {
		Key         string `json:"key"`
		Label       string `json:"label"`
		Value       string `json:"value"`
		FileValue   string `json:"file_value,omitempty"`
		Source      string `json:"source"`
		BotEditable bool   `json:"bot_editable,omitempty"`
		Risky       bool   `json:"risky,omitempty"`
		Hint        string `json:"hint,omitempty"`
	}
	type env struct {
		Name  string `json:"name"`
		Label string `json:"label"`
		Value string `json:"value,omitempty"`
	}
	out := struct {
		ConfigPath    string   `json:"config_path"`
		ConfigExists  bool     `json:"config_exists"`
		Args          []string `json:"args"`
		Items         []item   `json:"items"`
		Env           []env    `json:"env_set"`
		RestartNeeded bool     `json:"restart_needed"`
	}{ConfigPath: snap.ConfigPath, ConfigExists: snap.ConfigExists, Args: snap.Args, RestartNeeded: snap.RestartNeeded}
	for _, it := range snap.Items {
		entry := item{Key: it.Key, Label: it.Label, Value: it.Value, Source: string(it.Source), BotEditable: it.BotEditable, Risky: it.Risky, Hint: it.Hint}
		if it.Pending {
			entry.FileValue = it.FileValue
		}
		out.Items = append(out.Items, entry)
	}
	for _, e := range snap.Env {
		if !e.Set {
			continue
		}
		value := e.Value
		if e.Secret {
			value = "已设置"
		}
		out.Env = append(out.Env, env{Name: e.Name, Label: e.Label, Value: value})
	}
	body, err := json.Marshal(out)
	return string(body), err
}

// CanonicalOperation 是 Run 实际执行的操作：没写 operation 按 get 算，安全模式按它拦。
func (t *dianaRuntimeConfigTool) CanonicalOperation(input map[string]any) string {
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		return "get"
	}
	return operation
}
