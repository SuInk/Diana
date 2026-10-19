// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// tau-extract 把一条消息的 debug-traces 目录转成 taueval 用例骨架：
// 第一次 reply 请求的上下文、常驻与已加载工具的定义、生产实际发生的工具调用。
// 用户设定、初始状态、动作语义和断言仍需手写，见 docs/evals/tau.md。
//
//	go run ./cmd/tau-extract -id reminder-cancel <data>/debug-traces/<日期>/<会话>/<message_id> > case.json
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/agent/taueval"
	"github.com/SuInk/diana/model/llm"
)

type entry struct {
	Metadata struct {
		Phase      string         `json:"phase"`
		Purpose    string         `json:"purpose"`
		Tool       string         `json:"tool"`
		ToolInput  map[string]any `json:"tool_input"`
		ToolOutput string         `json:"tool_output"`
		Error      string         `json:"error"`
		Request    struct {
			Messages []llm.Message        `json:"messages"`
			Tools    []llm.ToolDefinition `json:"tools"`
		} `json:"request"`
	} `json:"metadata"`
}

var runnerTools = map[string]bool{"tools_load": true, "tools_execute": true, "agent_finalize": true}

func main() {
	id := flag.String("id", "", "case id (defaults to the message directory name)")
	schemas := flag.String("schemas", "", "JSONL of tools_load entries for tools loaded in earlier turns")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: tau-extract [-id name] <debug-traces message dir>")
		os.Exit(2)
	}
	extra, err := readSchemas(*schemas)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c, err := extract(flag.Arg(0), extra)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *id != "" {
		c.ID = *id
	}
	out := json.NewEncoder(os.Stdout)
	out.SetEscapeHTML(false)
	out.SetIndent("", "  ")
	if err := out.Encode(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func extract(dir string, extra map[string]llm.ToolDefinition) (taueval.Case, error) {
	c := taueval.Case{ID: filepath.Base(dir), Source: dir, User: taueval.UserSpec{Name: "群友", Instructions: "TODO: 用户想达成什么、知道哪些信息、何时结束"}}
	files, err := filepath.Glob(filepath.Join(dir, "*.json*"))
	if err != nil {
		return c, err
	}
	sort.Strings(files)
	defs := map[string]llm.ToolDefinition{}
	outputs := map[string]string{}
	for _, file := range files {
		e, err := read(file)
		if err != nil {
			return c, fmt.Errorf("%s: %w", file, err)
		}
		m := e.Metadata
		switch {
		case m.Purpose == "reply" && m.Phase == "model_request":
			for _, tool := range m.Request.Tools {
				if !runnerTools[tool.Name] {
					defs[tool.Name] = tool
				}
			}
			if c.Messages == nil {
				c.Messages = initialMessages(m.Request.Messages)
				for _, tool := range m.Request.Tools {
					if !runnerTools[tool.Name] {
						c.CoreTools = append(c.CoreTools, tool.Name)
					}
				}
			}
		case m.Phase == "agent_tool_completed" && m.Tool == "tools_load":
			var loaded struct {
				Loaded []loadedTool `json:"loaded"`
			}
			if json.Unmarshal([]byte(m.ToolOutput), &loaded) == nil {
				for _, tool := range loaded.Loaded {
					defs[tool.Name] = llm.ToolDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}
				}
			}
		case m.Phase == "agent_tool_completed" && !runnerTools[m.Tool]:
			c.Reference = append(c.Reference, taueval.Call{Tool: m.Tool, Input: m.ToolInput, Output: m.ToolOutput, Error: m.Error})
			if _, ok := outputs[m.Tool]; !ok {
				outputs[m.Tool] = m.ToolOutput
			}
			if _, ok := defs[m.Tool]; !ok {
				defs[m.Tool] = llm.ToolDefinition{Name: m.Tool}
			}
		}
	}
	if c.Messages == nil {
		return c, fmt.Errorf("%s has no reply model_request", dir)
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := defs[name]
		if def.Parameters == nil {
			if known, ok := extra[name]; ok {
				def = known
			} else {
				fmt.Fprintf(os.Stderr, "warning: %s has no schema; pass -schemas\n", name)
			}
		}
		output, ok := outputs[name]
		if !ok {
			output = `{"ok":true}`
		}
		c.Tools = append(c.Tools, taueval.ToolSpec{Name: name, Description: def.Description, Parameters: def.Parameters,
			Actions: map[string]taueval.ActionSpec{"*": {Op: "fixed", Output: output}}})
	}
	return c, nil
}

type loadedTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func readSchemas(path string) (map[string]llm.ToolDefinition, error) {
	defs := map[string]llm.ToolDefinition{}
	if path == "" {
		return defs, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var tool loadedTool
		if err := json.Unmarshal([]byte(line), &tool); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		defs[tool.Name] = llm.ToolDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}
	}
	return defs, nil
}

// initialMessages 只留系统提示词与用户消息；录制的图片地址早已过期，换成占位文字。
func initialMessages(messages []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range messages {
		if m.Role != llm.RoleSystem && m.Role != llm.RoleUser {
			continue
		}
		if len(m.Parts) > 0 {
			var text []string
			for _, part := range m.Parts {
				if part.Text != "" {
					text = append(text, part.Text)
				} else {
					text = append(text, "[图片]")
				}
			}
			if m.Content == "" {
				m.Content = strings.Join(text, "\n")
			}
			m.Parts = nil
		}
		out = append(out, llm.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func read(file string) (entry, error) {
	var e entry
	f, err := os.Open(file)
	if err != nil {
		return e, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(file, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return e, err
		}
		defer gz.Close()
		r = gz
	}
	err = json.NewDecoder(r).Decode(&e)
	return e, err
}
