// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/codemode"
	"github.com/SuInk/diana/internal/secretmask"
)

const CodeModeToolName = "codemode"
const codeModeToolName = CodeModeToolName

// codeModeChildResultBytes bounds one child result handed to a script. Scripts get
// the full masked result, not the model-facing truncation: that cut lands mid-JSON
// and appends a note, so the script received a string instead of an object
// (production llm_usage: 163K chars cut to 20K, Object.keys returned character
// indices and blew the 32 KiB output cap). Headroom stays under the 1 MiB frame.
const codeModeChildResultBytes = 512 * 1024

type codeModeTool struct {
	runtime codemode.Runtime
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

func newCodeModeTool() (*codeModeTool, error) {
	runtime, err := codemode.Prepare()
	if err != nil {
		return nil, err
	}
	return &codeModeTool{runtime: runtime}, nil
}

func (*codeModeTool) Name() string { return codeModeToolName }
func (*codeModeTool) Description() string {
	return "异步 JavaScript 编排：多步调用、循环或筛选大结果时用；只调一次就直接调原工具。await tools.<名字>({参数}) 调按需目录里的工具，参数照目录里括号内的签名填，不用 tool_search；结果是解析好的 JSON，结构不明先 return Object.keys(r)，只 return 需要的字段。例：const p=await tools.browser_render({url}); return p.title; store/load 跨脚本存小状态。仅 web_search/browser_render 可并发。无 Node/联网；发送、终止、图片在脚本外调用。"
}
func (*codeModeTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string", "description": "Async JavaScript function body; return the relevant fields, not entire tool responses."}, "fresh": map[string]any{"type": "boolean", "description": "Bypass this request's two-minute successful webpage-result cache."}}, "required": []string{"code"}, "additionalProperties": false}
}
func (*codeModeTool) Timeout() time.Duration { return 120 * time.Second }
func (*codeModeTool) Run(context.Context, map[string]any) (string, error) {
	return "", errors.New("codemode requires the Runner permission bridge")
}

// runCodeMode keeps child evidence in Steps, not in model tool messages. State
// transitions are serialized; only the existing read-only allowlist may overlap.
func (r *Runner) runCodeMode(ctx context.Context, tool *codeModeTool, input map[string]any, req Request, hooks *runHookState, traceID string, modelTurn int, toolCalls, introspectionCalls, loadCalls *int, lastSignature *string, steps *[]Step) (string, error) {
	if err := validateToolInput(tool.InputSchema(), input); err != nil {
		return "", err
	}
	names := r.registry.PresentedNames()
	// 脚本里 ALL_TOOLS 和「tools.X 不存在」的提示用得上签名：和按需目录同一份。
	descriptions := make(map[string]string, len(names)+1)
	for _, name := range names {
		if child, ok := r.registry.Get(name); ok {
			descriptions[name] = name + toolSignature(child) + ": " + catalogLineDescription(child)
		}
	}
	if r.loader != nil {
		names = append(names, ToolsLoadToolName)
		descriptions[ToolsLoadToolName] = ToolsLoadToolName + schemaSignature(r.loader.InputSchema()) + ": " + r.loader.Description()
	}
	var mu sync.Mutex
	cacheHits := 0
	fresh, _ := input["fresh"].(bool)
	result, err := tool.runtime.Execute(ctx, codemode.Request{Code: input["code"].(string), Store: r.codeModeStore, Tools: names, Descriptions: descriptions, ParallelTools: []string{WebSearchToolName, browserRenderToolName}, MaxCalls: 32, Timeout: 120 * time.Second}, func(callCtx context.Context, name string, raw json.RawMessage) (any, error) {
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
		action, err = r.dispatchAction(action)
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
				// 参数错了就把期望的形状一起给脚本，照 pi-codemode 的做法，免得模型再猜一轮。
				return deny(fmt.Errorf("%w；期望 tools.%s%s", err, action.Tool, toolSignature(child)))
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
			r.rememberCodeModeRead(cacheKey, action, read.rawOutput)
		}
		mu.Unlock()
		if read.err != nil {
			return nil, errors.New(read.record.Error)
		}
		output := read.rawOutput
		if len(output) > codeModeChildResultBytes {
			return nil, fmt.Errorf("%s returned %d bytes, over the %d-byte script limit; narrow the call with its paging, filter or scope parameters", action.Tool, len(output), codeModeChildResultBytes)
		}
		var value any
		if json.Unmarshal([]byte(output), &value) == nil {
			return value, nil
		}
		return output, nil
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
	result.Value = compactCodeModeValue(result.Value)
	return marshalCodeModeJSON(struct {
		codemode.Result
		CacheHits int `json:"cache_hits,omitempty"`
	}{result, cacheHits})
}

// compactCodeModeValue drops null object members before the value reaches model
// context. Scripts that return a tool response wholesale otherwise pay for fields
// like attempts:null and providers:null on every turn after.
func compactCodeModeValue(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte("null")) {
		return raw
	}
	compacted, err := dropJSONNulls(raw)
	if err != nil {
		return raw
	}
	return compacted
}

// dropJSONNulls walks raw values instead of decoding into maps so member order
// stays as the script wrote it. Array nulls are kept: they hold positions.
func dropJSONNulls(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteByte(raw[0])
	for count := 0; decoder.More(); {
		var key string
		if raw[0] == '{' {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, _ = token.(string)
		}
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return nil, err
		}
		if raw[0] == '{' && string(item) == "null" {
			continue
		}
		item, err := dropJSONNulls(item)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			out.WriteByte(',')
		}
		count++
		if raw[0] == '{' {
			encodedKey, err := marshalCodeModeJSON(key)
			if err != nil {
				return nil, err
			}
			out.WriteString(encodedKey)
			out.WriteByte(':')
		}
		out.Write(item)
	}
	out.WriteByte(raw[len(raw)-1])
	return out.Bytes(), nil
}

// marshalCodeModeJSON keeps <, > and & literal: json.Marshal turns each into a
// six-character \u escape, and page text is full of them.
func marshalCodeModeJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}
