// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package taueval

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// ToolSpec 是一个模拟工具。名字、描述和参数照录生产定义，行为按 input.action 分派到
// Actions；分派字段默认 action（tasks 之类用 operation 时写进 ActionField），
// 没有分派字段的调用用键 ""，找不到对应键时退回 "*"。
type ToolSpec struct {
	Name        string                `json:"name"`
	ActionField string                `json:"action_field,omitempty"`
	Description string                `json:"description"`
	Parameters  map[string]any        `json:"parameters,omitempty"`
	Actions     map[string]ActionSpec `json:"actions"`
}

// ActionSpec 的 Op：
//   - read：返回 Collection 当前内容；Output 非空时改返回固定文本
//   - append：按 Fields（空则取除 action 外的全部字段）追加一条记录，Key 缺省时生成
//   - update：按 Key 找到记录并合并字段
//   - remove：按 Key 删除记录
//   - fixed：不碰状态，返回 Output 或 Error
type ActionSpec struct {
	Op         string   `json:"op"`
	Collection string   `json:"collection,omitempty"`
	Key        string   `json:"key,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
}

var validOps = map[string]bool{"read": true, "append": true, "update": true, "remove": true, "fixed": true}

// Call 是环境收到的一次工具调用。
type Call struct {
	Tool   string         `json:"tool"`
	Input  map[string]any `json:"input"`
	Output string         `json:"output,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// Env 是一次试验独占的环境。
type Env struct {
	mu    sync.Mutex
	state map[string]any
	calls []Call
	next  int
}

func NewEnv(initial map[string]any) *Env {
	return &Env{state: cloneJSON(initial)}
}

func (e *Env) State() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneJSON(e.state)
}

func (e *Env) Calls() []Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Call(nil), e.calls...)
}

// Tools 生成登记给 Runner 的工具实例。
func (e *Env) Tools(specs []ToolSpec) []agent.Tool {
	tools := make([]agent.Tool, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, &envTool{env: e, spec: spec})
	}
	return tools
}

func (e *Env) apply(spec ToolSpec, input map[string]any) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	output, err := e.applyLocked(spec, input)
	call := Call{Tool: spec.Name, Input: cloneJSON(input), Output: output}
	if err != nil {
		call.Error = err.Error()
	}
	e.calls = append(e.calls, call)
	return output, err
}

func (e *Env) applyLocked(spec ToolSpec, input map[string]any) (string, error) {
	field := spec.ActionField
	if field == "" {
		field = "action"
	}
	name, _ := input[field].(string)
	action, ok := spec.Actions[name]
	if !ok {
		action, ok = spec.Actions["*"]
	}
	if !ok {
		return "", fmt.Errorf("%s 不支持 action=%q", spec.Name, name)
	}
	if action.Error != "" {
		return "", fmt.Errorf("%s", action.Error)
	}
	records, _ := e.state[action.Collection].([]any)
	switch action.Op {
	case "fixed":
		return action.Output, nil
	case "read":
		if action.Output != "" {
			return action.Output, nil
		}
		return marshal(map[string]any{"items": records}), nil
	case "append":
		record := map[string]any{}
		if len(action.Fields) == 0 {
			for k, v := range input {
				if k != field {
					record[k] = v
				}
			}
		} else {
			for _, k := range action.Fields {
				if v, ok := input[k]; ok {
					record[k] = v
				}
			}
		}
		if action.Key != "" && record[action.Key] == nil {
			e.next++
			record[action.Key] = fmt.Sprintf("%s-%d", action.Collection, e.next)
		}
		e.state[action.Collection] = append(records, cloneJSON(record))
		return marshal(map[string]any{"ok": true, "item": record}), nil
	case "update", "remove":
		want := fmt.Sprint(input[action.Key])
		for i, raw := range records {
			record, _ := raw.(map[string]any)
			if record == nil || fmt.Sprint(record[action.Key]) != want {
				continue
			}
			if action.Op == "remove" {
				e.state[action.Collection] = append(records[:i:i], records[i+1:]...)
				return marshal(map[string]any{"ok": true, "removed": want}), nil
			}
			for k, v := range input {
				if k != field && (len(action.Fields) == 0 || contains(action.Fields, k)) {
					record[k] = cloneJSON(v)
				}
			}
			return marshal(map[string]any{"ok": true, "item": record}), nil
		}
		return "", fmt.Errorf("没有 %s=%s 的记录", action.Key, want)
	}
	return "", fmt.Errorf("unknown op %q", action.Op)
}

type envTool struct {
	env  *Env
	spec ToolSpec
}

func (t *envTool) Name() string        { return t.spec.Name }
func (t *envTool) Description() string { return t.spec.Description }
func (t *envTool) InputSchema() map[string]any {
	if t.spec.Parameters == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	return t.spec.Parameters
}
func (t *envTool) Run(_ context.Context, input map[string]any) (string, error) {
	return t.env.apply(t.spec, input)
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

func marshal(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// cloneJSON 经 JSON 往返深拷贝，同时把数字统一成 float64，断言比较时不受来源影响。
func cloneJSON[T any](v T) T {
	var out T
	data, _ := json.Marshal(v)
	_ = json.Unmarshal(data, &out)
	if m, ok := any(&out).(*map[string]any); ok && *m == nil {
		*m = map[string]any{}
	}
	return out
}
