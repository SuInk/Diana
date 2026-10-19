// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package taueval 按 τ²-bench 的思路评测 Agent 主对话：真实请求做起点，
// 有状态的模拟工具当环境，模型扮演的用户把对话推下去，结束后核对环境终态，
// 多次试验算 pass^k。设计说明见 docs/evals/tau.md。
package taueval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// Case 是一条评测用例。Messages 是被测 Agent 第一轮看到的完整上下文（含系统提示词与历史），
// 一般由 tau-extract 从 debug_trace 的 reply 请求里原样抽出。
type Case struct {
	ID string `json:"id"`
	// Category 用来分类汇报，如 state、no_tool、research、persona。
	Category string        `json:"category,omitempty"`
	Source   string        `json:"source,omitempty"`
	Messages []llm.Message `json:"messages"`
	Tools    []ToolSpec    `json:"tools"`
	// CoreTools 与生产一致地常驻，其余工具要先 tools_load；为空时全部常驻。
	CoreTools []string `json:"core_tools,omitempty"`
	// LoadedTools 是生产会话在这一轮之前已经 tools_load 过的工具，评测时同样视为已加载。
	LoadedTools []string       `json:"loaded_tools,omitempty"`
	State       map[string]any `json:"initial_state,omitempty"`
	User        UserSpec       `json:"user"`
	Expect      Expect         `json:"expect"`
	MaxTurns    int            `json:"max_turns,omitempty"`
	MaxSteps    int            `json:"max_steps,omitempty"`
	ReplyPrefix string         `json:"reply_prefix,omitempty"`
	// Reference 是生产那一轮实际发生的工具调用，只供写断言时参考，不参与评分。
	Reference []Call `json:"reference,omitempty"`
}

// UserSpec 描述模拟用户。Instructions 只给用户模拟器看，被测 Agent 看不到；
// Script 非空时按脚本逐条发送，不调用模型，适合需要确定性的用例。
type UserSpec struct {
	Name         string   `json:"name"`
	Instructions string   `json:"instructions,omitempty"`
	Script       []string `json:"script,omitempty"`
}

// Expect 是终态断言。State 对环境终态做子集匹配，Exact 里列出的集合要求条数一致；
// Replies 里每条子串都必须在 Agent 某次回复中出现（τ² 的「传达信息」检查）。
type Expect struct {
	State map[string]any `json:"state,omitempty"`
	Exact []string       `json:"exact,omitempty"`
	// Calls 里每个模式都要配上一次不同的工具调用：tool 相同、input 子集匹配。
	// 用于没有可模拟状态、只能核对写操作参数的工具。
	Calls        []Call   `json:"calls,omitempty"`
	RequireTools []string `json:"require_tools,omitempty"`
	ForbidTools  []string `json:"forbid_tools,omitempty"`
	Replies      []string `json:"replies,omitempty"`
	Criteria     string   `json:"criteria,omitempty"`
}

// LoadCases 读取文件或目录（目录下所有 *.json）。
func LoadCases(path string) ([]Case, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	files := []string{path}
	if info.IsDir() {
		files, err = filepath.Glob(filepath.Join(path, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
	}
	var cases []Case
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		cases = append(cases, c)
	}
	return cases, nil
}

func (c Case) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("case id is empty")
	}
	if len(c.Messages) == 0 {
		return fmt.Errorf("case %s has no messages", c.ID)
	}
	if len(c.User.Script) == 0 && strings.TrimSpace(c.User.Instructions) == "" {
		return fmt.Errorf("case %s needs user.instructions or user.script", c.ID)
	}
	seen := map[string]bool{}
	for _, tool := range c.Tools {
		if seen[tool.Name] {
			return fmt.Errorf("case %s declares tool %s twice", c.ID, tool.Name)
		}
		seen[tool.Name] = true
		for name, action := range tool.Actions {
			if !validOps[action.Op] {
				return fmt.Errorf("case %s tool %s action %q has unknown op %q", c.ID, tool.Name, name, action.Op)
			}
		}
	}
	return nil
}
