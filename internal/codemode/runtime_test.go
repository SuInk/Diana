// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRuntime(t *testing.T) Runtime {
	t.Helper()
	_, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, cleanup, err := Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return runtime
}

func TestSupportedNodeVersions(t *testing.T) {
	for version, want := range map[string]bool{"v22.19.0": true, "v26.3.1": true, "v22.18.0": false, "v20.20.0": false, "junk": false, "v26.1.invalid": false} {
		if supportedNodeVersion(version) != want {
			t.Errorf("version %q", version)
		}
	}
}

func TestRuntimeConcurrentRPC(t *testing.T) {
	r := testRuntime(t)
	var started atomic.Int32
	ready := make(chan struct{})
	result, err := r.Execute(t.Context(), Request{
		Code:  `return await Promise.all([1,2].map(id => tools.read({id})));`,
		Tools: []string{"read"}, ParallelTools: []string{"read"}, MaxCalls: 2, Timeout: 2 * time.Second,
	}, func(ctx context.Context, name string, input json.RawMessage) (any, error) {
		if started.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return input, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || string(result.Value) != `[{"id":1},{"id":2}]` {
		t.Fatalf("result: %+v", result)
	}
}

func TestRuntimeCancellationWaitsForTools(t *testing.T) {
	r := testRuntime(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stopped atomic.Bool
	_, err := r.Execute(ctx, Request{Code: `await tools.wait({});`, Tools: []string{"wait"}, MaxCalls: 1, Timeout: 2 * time.Second},
		func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			cancel()
			<-ctx.Done()
			stopped.Store(true)
			return nil, ctx.Err()
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
	if !stopped.Load() {
		t.Fatal("tool goroutine outlived Execute")
	}
}

func TestRuntimeLimits(t *testing.T) {
	r := testRuntime(t)
	for _, tc := range []struct{ name, code, kind string }{
		{"timeout", `while (true) {}`, "timeout"},
		{"output", `return 'x'.repeat(40000)`, "output_limit"},
		{"budget", `try { await tools.read({}); } catch {} return 1`, "host_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := r.Execute(t.Context(), Request{Code: tc.code, Tools: []string{"read"}, Timeout: 150 * time.Millisecond}, func(context.Context, string, json.RawMessage) (any, error) {
				t.Error("unexpected host tool call")
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.OK || result.Error == nil || result.Error.Kind != tc.kind {
				t.Fatalf("result: %+v", result)
			}
		})
	}
}

func TestRuntimeSanitizedEnvironmentAndOversizedReply(t *testing.T) {
	r := testRuntime(t)
	// If inherited, this prevents the trusted host from even starting.
	t.Setenv("NODE_OPTIONS", "--require=/nonexistent/diana-must-not-load.cjs")
	result, err := r.Execute(t.Context(), Request{
		Code:  `try { await tools.read({}); } catch (e) { return e.message; }`,
		Tools: []string{"read"}, MaxCalls: 1, Timeout: time.Second,
	}, func(context.Context, string, json.RawMessage) (any, error) {
		return strings.Repeat("x", maxFrameBytes+1), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !strings.Contains(string(result.Value), "cannot fit") {
		t.Fatalf("result: %+v", result)
	}
}

func TestRuntimeSerialCallsKeepRPCOrder(t *testing.T) {
	r := testRuntime(t)
	var order []int
	result, err := r.Execute(t.Context(), Request{Code: `return await Promise.all([1,2,3,4].map(id => tools.write({id})));`, Tools: []string{"write"}, MaxCalls: 4, Timeout: time.Second}, func(ctx context.Context, _ string, input json.RawMessage) (any, error) {
		var args struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, err
		}
		order = append(order, args.ID)
		time.Sleep(5 * time.Millisecond)
		return args.ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !reflect.DeepEqual(order, []int{1, 2, 3, 4}) {
		t.Fatalf("order=%v result=%+v", order, result)
	}
}
