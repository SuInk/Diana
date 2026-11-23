// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package codemode runs model scripts in an in-process moejs runtime and
// bridges their tool calls to capability-scoped Go callbacks.
package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/SuInk/diana/internal/safego"
	"github.com/SuInk/diana/internal/secretmask"
)

const (
	maxFrameBytes    = 1024 * 1024
	maxArgumentBytes = 64 * 1024
	maxStoreBytes    = 512 * 1024
)

// Call must enforce caller permissions and honor cancellation. The runtime
// handles transport only; registering a name does not authorize its execution.
type Call func(context.Context, string, json.RawMessage) (any, error)

// Runtime executes scripts in process; the zero value is ready once Prepare
// has compiled the embedded prelude.
type Runtime struct{}

type Request struct {
	Store map[string]json.RawMessage
	Code  string
	Tools []string
	// Descriptions is what a script sees in ALL_TOOLS for each tool: its
	// signature and a line of purpose. Missing entries render as empty.
	Descriptions map[string]string
	// ParallelTools is an explicit read-only allowlist. Other calls run in call order.
	ParallelTools []string
	MaxCalls      int
	Timeout       time.Duration
}

type Result struct {
	// Store is transport-only; callers must never include it in model history.
	Store  map[string]json.RawMessage `json:"store,omitempty"`
	OK     bool                       `json:"ok"`
	Value  json.RawMessage            `json:"value,omitempty"`
	Output []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"output,omitempty"`
	Error *struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Prepare compiles the embedded prelude once per process. It needs no Node,
// npm or network.
func Prepare() (Runtime, error) {
	if _, err := compilePrelude(); err != nil {
		return Runtime{}, fmt.Errorf("codemode: compile prelude: %w", err)
	}
	return Runtime{}, nil
}

func failure(kind, message string) Result {
	result := Result{}
	result.Error = &struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}{kind, message}
	return result
}

func validTools(names []string) bool {
	if len(names) > 1024 {
		return false
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || len(name) > 128 || name == "__proto__" || name == "constructor" || name == "prototype" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

// scriptIdentifier is the name a script uses for a tool: characters that are
// not valid in a JavaScript identifier become "_", as in pi-codemode.
func scriptIdentifier(name string) string {
	var out strings.Builder
	for _, r := range name {
		valid := r == '_' || r == '$' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || out.Len() > 0 && r >= '0' && r <= '9'
		if valid {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "_"
	}
	return out.String()
}

type toolReply struct {
	id      int
	ok      bool
	payload *string
}

func (r Runtime) Execute(ctx context.Context, req Request, call Call) (Result, error) {
	if len(req.Code) > 64*1024 || req.MaxCalls < 0 || req.MaxCalls > 32 || call == nil || !validTools(req.Tools) {
		return Result{}, errors.New("invalid codemode configuration")
	}
	if req.Timeout <= 0 || req.Timeout > 120*time.Second {
		return Result{}, errors.New("codemode timeout must be within 1ms..120s")
	}
	storeJSON, err := json.Marshal(storeText(req.Store))
	if err != nil || len(storeJSON) > maxStoreBytes*2 {
		return Result{}, errors.New("invalid or oversized store")
	}
	allowed := make(map[string]bool, len(req.Tools))
	type toolEntry struct {
		Name        string `json:"name"`
		JSName      string `json:"jsName"`
		Description string `json:"description"`
	}
	entries := make([]toolEntry, 0, len(req.Tools))
	for _, name := range req.Tools {
		allowed[name] = true
		entries = append(entries, toolEntry{Name: name, JSName: scriptIdentifier(name), Description: req.Descriptions[name]})
	}
	toolsJSON, _ := json.Marshal(entries)
	parallel := make(map[string]bool, len(req.ParallelTools))
	for _, name := range req.ParallelTools {
		parallel[name] = true
	}

	// The script's own deadline yields a structured timeout result; the grace
	// period only bounds tool callbacks that ignore cancellation.
	ctx, cancel := context.WithTimeout(ctx, req.Timeout+2*time.Second)
	defer cancel()
	vmCtx, stopVM := context.WithCancel(ctx)
	defer stopVM()
	preludeScript, err := compilePrelude()
	if err != nil {
		return Result{}, err
	}
	machine := newVM(vmCtx, time.Now().Add(req.Timeout))
	defer machine.close()

	toolCtx, stopTools := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		stopTools()
		workers.Wait()
	}()
	replies := make(chan toolReply, req.MaxCalls)
	pending := map[int]bool{}
	var execution sync.RWMutex
	previous := make(chan struct{})
	close(previous)
	var (
		immediate  []toolReply
		output     []string
		stopReason string
		finished   bool
		doneOK     bool
		donePayload,
		doneWrites string
		doneHasValue bool
	)
	dispatch := func(id int, name string, input json.RawMessage) {
		prior := previous
		admitted := make(chan struct{})
		previous = admitted
		pending[id] = true
		workers.Add(1)
		go func() {
			defer recoverCallPanic(cancel)
			defer workers.Done()
			<-prior
			if parallel[name] {
				execution.RLock()
				defer execution.RUnlock()
			} else {
				execution.Lock()
				defer execution.Unlock()
			}
			close(admitted)
			if toolCtx.Err() != nil {
				return
			}
			value, callErr := call(toolCtx, name, input)
			reply := toolReply{id: id, ok: callErr == nil}
			if callErr != nil {
				message := callErr.Error()
				reply.payload = &message
			} else if value != nil {
				data, err := json.Marshal(value)
				if err != nil || len(data) > maxFrameBytes {
					// Serialization/size failures are ordinary tool errors, not silent hangs.
					message := "tool result cannot fit in codemode transport"
					reply.ok, reply.payload = false, &message
				} else {
					text := string(data)
					reply.payload = &text
				}
			}
			select {
			case replies <- reply:
			case <-toolCtx.Done():
			}
		}()
	}
	machine.bridge = func(kind string, args []moejs.Value) {
		if finished || stopReason != "" {
			return
		}
		switch kind {
		case "call":
			if len(args) < 2 {
				stopReason = "malformed bridge call"
				return
			}
			id := int(args[0].AsNumber())
			name := machine.toString(args[1])
			if id > req.MaxCalls {
				stopReason = "script tool-call budget exceeded"
				machine.stop()
				return
			}
			var input string
			if len(args) > 2 && !args[2].IsUndefined() {
				input = machine.toString(args[2])
			}
			trimmed := strings.TrimSpace(input)
			if !allowed[name] {
				message := "tool " + name + " is not available in this script; ALL_TOOLS lists the tools"
				immediate = append(immediate, toolReply{id: id, payload: &message})
				return
			}
			if len(input) > maxArgumentBytes || !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
				message := "tool arguments must be a bounded JSON object"
				immediate = append(immediate, toolReply{id: id, payload: &message})
				return
			}
			dispatch(id, name, json.RawMessage(trimmed))
		case "output":
			if len(args) < 2 {
				return
			}
			if machine.toString(args[0]) == "image" {
				output = append(output, "[image omitted: script output is text only]")
			} else {
				output = append(output, machine.toString(args[1]))
			}
		case "done":
			if len(args) < 3 {
				stopReason = "malformed bridge result"
				return
			}
			finished = true
			doneOK = args[0].IsTrue()
			if doneHasValue = !args[1].IsUndefined(); doneHasValue {
				donePayload = machine.toString(args[1])
			}
			if doneOK && !args[2].IsUndefined() {
				doneWrites = machine.toString(args[2])
			}
		}
	}

	timeout := func() Result {
		return failure("timeout", fmt.Sprintf("Execution timed out after %d ms", req.Timeout.Milliseconds()))
	}
	sandboxFailure := func(err error) (Result, error) {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if stopReason != "" {
			return failure("host_limit", stopReason), nil
		}
		if errors.Is(err, moejs.ErrMemoryLimit) {
			// QuickJS threw a catchable out-of-memory error here; moejs stops the
			// script outright, which the model sees as the same script failure.
			return failure("script", "out of memory: the script exceeded its 32 MiB heap"), nil
		}
		if machine.interrupted() {
			return timeout(), nil
		}
		return failure("sandbox", secretmask.Text(err.Error())), nil
	}

	undefined := moejs.Undefined()
	prelude, err := machine.rt.RunScript(preludeScript)
	if err != nil {
		return sandboxFailure(fmt.Errorf("prelude failed: %w", err))
	}
	api := machine.call(prelude, undefined, machine.bridgeFunction(), moejs.String(string(toolsJSON)), moejs.String("[]"), moejs.String(string(storeJSON)))
	settle, run, stalled := machine.get(api, "settle"), machine.get(api, "run"), machine.get(api, "stalled")
	if machine.err != nil {
		return sandboxFailure(machine.err)
	}
	// The prefix shares the first line with the script so reported line numbers
	// match the script as written.
	if script, err := moejs.CompileScript("codemode.js", "(async (tools, console) => {"+req.Code+"\n})"); err != nil {
		finished, doneOK, doneHasValue = true, false, true
		donePayload = describeError(err)
	} else if fn, err := machine.rt.RunScript(script); err != nil {
		finished, doneOK, doneHasValue = true, false, true
		donePayload = describeError(err)
	} else {
		machine.call(run, api, fn)
	}
	// moejs runs the promise jobs a call queues before the call returns, so
	// after each step the script is either finished or waiting on a tool.
	step := func() error {
		if !finished && stopReason == "" {
			machine.call(stalled, api)
		}
		return machine.err
	}
	if err := step(); err != nil {
		return sandboxFailure(err)
	}
	if machine.err != nil {
		return sandboxFailure(machine.err)
	}
	deadline := time.NewTimer(time.Until(machine.deadline))
	defer deadline.Stop()
	for !finished && stopReason == "" {
		var reply toolReply
		if len(immediate) > 0 {
			reply, immediate = immediate[0], immediate[1:]
		} else {
			select {
			case reply = <-replies:
			case <-deadline.C:
				return timeout(), nil
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		delete(pending, reply.id)
		payload := undefined
		if reply.payload != nil {
			payload = moejs.String(*reply.payload)
		}
		machine.call(settle, api, engine.IntValue(reply.id), engine.Bool(reply.ok), payload)
		if err := step(); err != nil {
			return sandboxFailure(err)
		}
	}
	if stopReason != "" {
		return failure("host_limit", stopReason), nil
	}
	if machine.interrupted() && !doneOK {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return timeout(), nil
	}
	return finish(doneOK, doneHasValue, donePayload, doneWrites, output, req.Store)
}

// finish shapes the model-facing result: text output only (the embedded prelude
// caps runaway output like Pi; agent bounds what the model sees), and the store
// merged with this run's writes.
func finish(ok, hasValue bool, payload, writes string, output []string, previous map[string]json.RawMessage) (Result, error) {
	var result Result
	for _, text := range output {
		result.Output = append(result.Output, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", text})
	}
	if !ok {
		var scriptError struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &scriptError); err != nil {
			return failure("sandbox", "Sandbox bridge broken: script error is not valid JSON"), nil
		}
		failed := failure("script", scriptError.Message)
		result.Error = failed.Error
	} else {
		result.OK = true
		if hasValue {
			if !json.Valid([]byte(payload)) {
				return failure("sandbox", "Sandbox bridge broken: return value is not valid JSON"), nil
			}
			result.Value = json.RawMessage(payload)
		}
	}
	if !ok {
		return result, nil
	}
	store := make(map[string]json.RawMessage, len(previous))
	for key, value := range previous {
		store[key] = value
	}
	if writes != "" {
		var entries [][]string
		if err := json.Unmarshal([]byte(writes), &entries); err != nil {
			return failure("sandbox", "Sandbox bridge broken: store writes are not valid"), nil
		}
		for _, entry := range entries {
			switch {
			case len(entry) == 1:
				delete(store, entry[0])
			case len(entry) == 2 && json.Valid([]byte(entry[1])):
				store[entry[0]] = json.RawMessage(entry[1])
			default:
				return failure("sandbox", "Sandbox bridge broken: store writes are not valid"), nil
			}
		}
	}
	if data, err := json.Marshal(store); err != nil || len(data) > maxStoreBytes {
		return failure("store_limit", "store exceeds 524288 bytes; delete unused keys with store(key, undefined)"), nil
	}
	result.Store = store
	return result, nil
}

// storeText hands the prelude each value as JSON text, the form load() parses.
func storeText(store map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(store))
	for key, value := range store {
		var compact bytes.Buffer
		if json.Compact(&compact, value) == nil {
			out[key] = compact.String()
		}
	}
	return out
}

// describeError shapes a compile failure like the prelude's script errors.
func describeError(err error) string {
	name, message := "Error", err.Error()
	var syntax *moejs.SyntaxError
	var exception *moejs.Exception
	switch {
	case errors.As(err, &syntax):
		name, message = "SyntaxError", syntax.Message
	case errors.As(err, &exception):
		name, message = exception.Name(), exception.Message()
	}
	data, _ := json.Marshal(map[string]string{"name": name, "message": message})
	return string(data)
}

func recoverCallPanic(cancel context.CancelFunc) {
	if value := recover(); value != nil {
		// Hooks are caller-supplied too. Stop the VM instead of leaving its call
		// unresolved or allowing a callback panic to kill the entire process.
		cancel()
		safego.Report("codemode.tool_callback", secretmask.Text(fmt.Sprint(value)))
	}
}
