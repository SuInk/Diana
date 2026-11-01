// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package codemode bridges a trusted Node host to capability-scoped Go tools.
// The host executes model scripts in QuickJS, never in the Node context.
package codemode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
)

const maxFrameBytes = 1024 * 1024

// Call must enforce caller permissions and honor cancellation. The runtime
// handles transport only; registering a name does not authorize its execution.
type Call func(context.Context, string, json.RawMessage) (any, error)

type Runtime struct {
	Node string
	Host string
}

type Request struct {
	Store map[string]json.RawMessage
	Code  string
	Tools []string
	// ParallelTools is an explicit read-only allowlist. Other calls run in RPC order.
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

type boundedLog struct{ data []byte }

func hostEnvironment() []string {
	env := []string{}
	for _, key := range []string{"PATH", "TMPDIR", "TMP", "TEMP", "SystemRoot"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (b *boundedLog) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(remaining, n)]...)
	}
	return n, nil
}

func (r Runtime) Execute(ctx context.Context, req Request, call Call) (Result, error) {
	var result Result
	if !filepath.IsAbs(r.Host) || len(req.Code) > 64*1024 || req.MaxCalls < 0 || req.MaxCalls > 32 || call == nil {
		return result, errors.New("invalid codemode configuration")
	}
	if req.Timeout <= 0 || req.Timeout > 120*time.Second {
		return result, errors.New("codemode timeout must be within 1ms..120s")
	}
	allowed := make(map[string]bool, len(req.Tools))
	if req.Tools == nil {
		req.Tools = []string{}
	}
	for _, name := range req.Tools {
		allowed[name] = true
	}
	// Allow a bounded grace period for the VM's structured timeout result.
	ctx, cancel := context.WithTimeout(ctx, req.Timeout+2*time.Second)
	defer cancel()
	node := r.Node
	if node == "" {
		node = "node"
	}
	cmd := procgroup.CommandContext(ctx, node, r.Host)
	// Do not inherit credentials or NODE_OPTIONS/NODE_PATH code injection.
	cmd.Env = hostEnvironment()
	var stderr boundedLog
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return result, err
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return result, err
	}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_ = stdin.Close()
		_ = procgroup.Kill(cmd)
		workers.Wait()
		_ = cmd.Wait()
	}()
	var writeMu sync.Mutex
	write := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(data) > maxFrameBytes {
			return errors.New("codemode reply exceeds frame limit")
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = stdin.Write(append(data, '\n'))
		return err
	}
	startFrame := map[string]any{"type": "start", "code": req.Code, "tools": req.Tools, "max_calls": req.MaxCalls, "timeout_ms": max(1, req.Timeout.Milliseconds())}
	if req.Store != nil {
		startFrame["store"] = req.Store
	}
	if err := write(startFrame); err != nil {
		return result, err
	}
	toolCtx, stopTools := context.WithCancel(ctx)
	defer stopTools()
	pending := make(map[int]context.CancelFunc)
	defer func() {
		for _, stop := range pending {
			stop()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes+1)
	lastID := 0
	var execution sync.RWMutex
	previous := make(chan struct{})
	close(previous)
	for scanner.Scan() {
		var frame struct {
			Type  string          `json:"type"`
			ID    int             `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return result, fmt.Errorf("invalid codemode frame: %w", err)
		}
		switch frame.Type {
		case "call":
			if frame.ID <= lastID || frame.ID > req.MaxCalls || !allowed[frame.Name] || len(frame.Input) > 64*1024 || !strings.HasPrefix(strings.TrimSpace(string(frame.Input)), "{") {
				return result, errors.New("invalid codemode tool request")
			}
			lastID = frame.ID
			callCtx, stop := context.WithCancel(toolCtx)
			pending[frame.ID] = stop
			prior := previous
			admitted := make(chan struct{})
			previous = admitted
			parallel := false
			for _, name := range req.ParallelTools {
				if name == frame.Name {
					parallel = true
					break
				}
			}
			workers.Add(1)
			go func(id int, name string, input json.RawMessage) {
				defer workers.Done()
				defer stop()
				<-prior
				if parallel {
					execution.RLock()
					defer execution.RUnlock()
				} else {
					execution.Lock()
					defer execution.Unlock()
				}
				close(admitted)
				if callCtx.Err() != nil {
					return
				}
				value, callErr := call(callCtx, name, input)
				reply := map[string]any{"type": "tool_result", "id": id, "ok": callErr == nil, "value": value}
				if callErr != nil {
					delete(reply, "value")
					reply["error"] = callErr.Error()
				}
				if err := write(reply); err != nil {
					// Serialization/size failures are ordinary tool errors, not silent hangs.
					if err := write(map[string]any{"type": "tool_result", "id": id, "ok": false, "error": "tool result cannot fit in codemode transport"}); err != nil {
						cancel()
					}
				}
			}(frame.ID, frame.Name, append(json.RawMessage(nil), frame.Input...))
		case "cancel":
			if stop := pending[frame.ID]; stop != nil {
				stop()
			}
		case "result":
			if len(scanner.Bytes()) > (512+33)*1024 {
				return result, errors.New("codemode output exceeds limit")
			}
			if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
				return result, err
			}
			store := result.Store
			result.Store = nil
			modelOutput, err := json.Marshal(result)
			if err != nil || len(modelOutput) > 33*1024 {
				return Result{}, errors.New("codemode model output exceeds limit")
			}
			stored, err := json.Marshal(store)
			if err != nil || len(stored) > 512*1024 {
				return Result{}, errors.New("codemode store exceeds limit")
			}
			result.Store = store
			return result, nil
		default:
			return result, errors.New("unexpected codemode frame type")
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	// stderr can contain host paths; leave disclosure policy to the caller.
	return result, fmt.Errorf("codemode host ended without a result: %w", io.ErrUnexpectedEOF)
}
