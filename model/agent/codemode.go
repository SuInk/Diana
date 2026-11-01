// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/codemode"
	"github.com/SuInk/diana/internal/secretmask"
)

const CodeModeToolName = "codemode"
const codeModeToolName = CodeModeToolName

type codeModeTool struct {
	runtime codemode.Runtime
	cleanup func()
}

type codeModeReadSnapshot struct {
	output  string
	created time.Time
}

func (r *Runner) rememberCodeModeRead(key string, action llmAction, output string) {
	if action.Tool != browserRenderToolName {
		return
	}
	pages := browserRenderPages(output)
	if len(pages) == 0 || len(pages) != len(browserRenderURLs(action.Input)) {
		return
	}
	for _, page := range pages {
		if page.SourceStage != "page_read" || page.Title == "" || page.Text == "" {
			return
		}
	}
	if r.codeModeReads == nil {
		r.codeModeReads = map[string]codeModeReadSnapshot{}
	}
	bytes := len(output)
	for name, entry := range r.codeModeReads {
		if time.Since(entry.created) > 2*time.Minute {
			delete(r.codeModeReads, name)
			continue
		}
		if name != key {
			bytes += len(entry.output)
		}
	}
	if bytes > 512*1024 || len(r.codeModeReads) >= 32 {
		return
	}
	r.codeModeReads[key] = codeModeReadSnapshot{output: output, created: time.Now()}
}

func newCodeModeTool(ctx context.Context) (*codeModeTool, error) {
	runtime, cleanup, err := codemode.Prepare(ctx)
	if err != nil {
		return nil, err
	}
	return &codeModeTool{runtime: runtime, cleanup: cleanup}, nil
}

func (*codeModeTool) Name() string { return codeModeToolName }
func (*codeModeTool) Description() string {
	return "异步 JavaScript 编排工具。const p=await tools.browser_render({url:'https://example.com'}); store('p',p); return p.title; 后续 load('p') 可重筛；store/load 是全局函数，不是 tools 方法，限512KiB、仅本轮。相同网页读取短期复用，fresh:true 强制重读。参数不明先 tool_search；仅 web_search/browser_render 可并发。权限预算不变，无 Node/文件/直接联网；发送、终止、图片在脚本外调用。"
}
func (*codeModeTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string", "description": "Async JavaScript function body; return the relevant fields, not entire tool responses."}, "fresh": map[string]any{"type": "boolean", "description": "Bypass this request's two-minute successful webpage-result cache."}}, "required": []string{"code"}, "additionalProperties": false}
}
func (*codeModeTool) Timeout() time.Duration { return 120 * time.Second }
func (*codeModeTool) Run(context.Context, map[string]any) (string, error) {
	return "", errors.New("codemode requires the Runner permission bridge")
}
func (t *codeModeTool) Close() error { t.cleanup(); return nil }

// runCodeMode keeps child evidence in Steps, not in model tool messages. State
// transitions are serialized; only the existing read-only allowlist may overlap.
func (r *Runner) runCodeMode(ctx context.Context, tool *codeModeTool, input map[string]any, req Request, hooks *runHookState, traceID string, modelTurn int, toolCalls, introspectionCalls, loadCalls *int, lastSignature *string, steps *[]Step) (string, error) {
	if err := validateToolInput(tool.InputSchema(), input); err != nil {
		return "", err
	}
	names := r.registry.Names()
	if r.loader != nil {
		names = append(names, ToolsLoadToolName)
	}
	var mu sync.Mutex
	cacheHits := 0
	fresh, _ := input["fresh"].(bool)
	result, err := tool.runtime.Execute(ctx, codemode.Request{Code: input["code"].(string), Store: r.codeModeStore, Tools: names, ParallelTools: []string{WebSearchToolName, browserRenderToolName}, MaxCalls: 32, Timeout: 120 * time.Second}, func(callCtx context.Context, name string, raw json.RawMessage) (any, error) {
		mu.Lock()
		action := llmAction{Action: "tool", Tool: name}
		if err := json.Unmarshal(raw, &action.Input); err != nil {
			mu.Unlock()
			return nil, err
		}
		deny := func(err error) (any, error) {
			record := Step{Index: len(*steps) + 1, Tool: name, Input: action.Input, Error: secretmask.Text(err.Error()), Skipped: true}
			*steps = append(*steps, record)
			mu.Unlock()
			hooks.after(callCtx, record)
			return nil, errors.New(record.Error)
		}
		if err := callCtx.Err(); err != nil {
			return deny(err)
		}
		if name == codeModeToolName || name == finalizeToolName || name == ToolsExecuteToolName {
			return deny(errors.New("nested dispatch and send are not available inside codemode"))
		}
		var err error
		action, err = r.loader.dispatch(action)
		if err != nil {
			return deny(err)
		}
		child, ok := r.registry.Get(action.Tool)
		if !ok && r.loader != nil && action.Tool == ToolsLoadToolName {
			child, ok = r.loader, true
		}
		if !ok {
			return deny(fmt.Errorf("tool %q is not available", action.Tool))
		}
		if _, terminal := child.(TerminalResultTool); terminal {
			return deny(errors.New("terminal tools must run outside codemode"))
		}
		if _, rich := child.(ToolResultPartsTool); rich {
			return deny(errors.New("rich-result tools must run outside codemode"))
		}
		if typed, ok := child.(ToolInputSchema); ok {
			action.Input = coerceToolInputArrays(typed.InputSchema(), action.Input)
			if err := validateToolInput(typed.InputSchema(), action.Input); err != nil {
				return deny(err)
			}
		}
		action.Input = resolveSearchRefs(action.Tool, action.Input, *steps)
		if err := r.registry.OperationDisabledError(action.Tool, action.Input); err != nil {
			return deny(err)
		}
		if kind := explicitUserRequestKind(child, action.Input); kind != "" && !ExtensionMutationAuthorized(currentUserRequestText(req), kind, action.Tool, action.Input) {
			code := extensionMutationConfirmationCode(kind, action.Tool, action.Input)
			return deny(errors.New(extensionMutationConfirmationPrompt(kind, action.Tool, code, action.Input)))
		}
		action.Input = minimalToolInput(action.Tool, action.Input)
		signature := toolCallSignature(action.Tool, action.Input)
		cacheKey := signature
		cached, cacheHit := r.codeModeReads[cacheKey]
		cacheHit = cacheHit && !fresh && action.Tool == browserRenderToolName && time.Since(cached.created) <= 2*time.Minute
		if fresh && action.Tool == browserRenderToolName {
			delete(r.codeModeReads, cacheKey)
		}
		if repeatable, ok := child.(RepeatableTool); ok && repeatable.RepeatableCalls() {
			signature = ""
		}
		if signature != "" && signature == *lastSignature && !cacheHit && !(fresh && action.Tool == browserRenderToolName) {
			return deny(errors.New("consecutive duplicate tool call skipped; use its previous result"))
		}
		if isIntrospectionCall(child, action.Input) {
			if *introspectionCalls >= maxIntrospectionCallsPerAgentRun {
				return deny(errors.New("introspection budget exhausted"))
			}
			*introspectionCalls++
		} else if action.Tool == ToolsLoadToolName {
			if *loadCalls >= maxToolLoadCallsPerAgentRun {
				return deny(errors.New("tool search budget exhausted"))
			}
			*loadCalls++
		} else {
			if r.remainingToolCalls(*toolCalls) == 0 {
				return deny(errors.New("tool budget exhausted"))
			}
			*toolCalls++
		}
		*lastSignature = signature
		ordinal := *toolCalls
		read := parallelRead{tool: child, action: action}
		if cacheHit {
			read.cachedOutput = &cached.output
		}
		mu.Unlock()
		r.runParallelRead(callCtx, hooks, req.Observer, traceID, modelTurn, ordinal, &read)
		mu.Lock()
		read.record.Index = len(*steps) + 1
		*steps = append(*steps, read.record)
		if read.record.CacheHit {
			cacheHits++
		}
		if read.err == nil && !read.record.CacheHit {
			r.rememberCodeModeRead(cacheKey, action, read.output)
		}
		mu.Unlock()
		if read.err != nil {
			return nil, errors.New(read.record.Error)
		}
		var value any
		if json.Unmarshal([]byte(read.output), &value) == nil {
			return value, nil
		}
		return read.output, nil
	})
	if err != nil {
		return "", err
	}
	if !result.OK {
		if result.Error != nil {
			return "", fmt.Errorf("codemode %s: %s", result.Error.Kind, result.Error.Message)
		}
		return "", errors.New("codemode script failed")
	}
	r.codeModeStore = result.Store
	result.Store = nil
	encoded, err := json.Marshal(struct {
		codemode.Result
		CacheHits int `json:"cache_hits,omitempty"`
	}{result, cacheHits})
	return string(encoded), err
}
