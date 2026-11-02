// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRuntime(t *testing.T) Runtime {
	t.Helper()
	runtime, err := Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return runtime
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
			timeout := 2 * time.Second
			if tc.kind == "timeout" {
				timeout = 150 * time.Millisecond
			}
			result, err := r.Execute(t.Context(), Request{Code: tc.code, Tools: []string{"read"}, Timeout: timeout}, func(context.Context, string, json.RawMessage) (any, error) {
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

func TestRuntimeCallbackPanicCancelsExecution(t *testing.T) {
	r := testRuntime(t)
	_, err := r.Execute(t.Context(), Request{
		Code: `return await tools.read({});`, Tools: []string{"read"}, MaxCalls: 1, Timeout: 2 * time.Second,
	}, func(context.Context, string, json.RawMessage) (any, error) {
		panic("callback failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("callback panic must cancel the runtime: %v", err)
	}
}

func TestRuntimeOversizedReply(t *testing.T) {
	r := testRuntime(t)
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

func noTools(t *testing.T) Call {
	return func(context.Context, string, json.RawMessage) (any, error) {
		t.Error("unexpected host tool call")
		return nil, nil
	}
}

func TestRuntimeScriptSemantics(t *testing.T) {
	r := testRuntime(t)
	for _, tc := range []struct{ name, code, value, kind, output string }{
		{name: "no host globals", code: `return [typeof process, typeof require, typeof fetch, typeof WebAssembly, typeof setTimeout, ({}).constructor.constructor('return typeof process')()]`, value: `["undefined","undefined","undefined","undefined","undefined","undefined"]`},
		{name: "unicode and json", code: `return {s: "中文<b>&", n: 1.5, a: [null, true]}`, value: `{"s":"中文<b>&","n":1.5,"a":[null,true]}`},
		{name: "undefined result", code: `const x = 1;`},
		{name: "console output", code: `console.log("a", {b: 1}); text(2); return 1`, value: `1`, output: `a {"b":1}|2`},
		{name: "syntax error", code: `return {`, kind: "script"},
		{name: "thrown error", code: `throw new TypeError("bad")`, kind: "script"},
		{name: "missing tool", code: `return await tools.not_registered({})`, kind: "script"},
		{name: "deep recursion is catchable", code: `function f(n){return f(n+1)+1} try { f(0) } catch (e) { return e.name }`, value: `"RangeError"`},
		{name: "memory is bounded", code: `const a = []; for (;;) a.push("payload".repeat(1000) + a.length)`, kind: "script"},
		{name: "never settles", code: `await new Promise(() => {})`, kind: "script"},
		{name: "exit", code: `text("x"); exit(); return 2`, output: "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := r.Execute(t.Context(), Request{Code: tc.code, Timeout: 5 * time.Second}, noTools(t))
			if err != nil {
				t.Fatal(err)
			}
			if tc.kind != "" {
				if result.OK || result.Error == nil || result.Error.Kind != tc.kind {
					t.Fatalf("result: %+v", result)
				}
				return
			}
			var texts []string
			for _, item := range result.Output {
				texts = append(texts, item.Text)
			}
			if !result.OK || string(result.Value) != tc.value || strings.Join(texts, "|") != tc.output {
				t.Fatalf("result: %+v error=%+v", result, result.Error)
			}
		})
	}
}

func TestRuntimeRecoversAfterTimeout(t *testing.T) {
	r := testRuntime(t)
	for _, code := range []string{`while (true) {}`, `while (true) await null`} {
		result, err := r.Execute(t.Context(), Request{Code: code, Timeout: 150 * time.Millisecond}, noTools(t))
		if err != nil || result.Error == nil || result.Error.Kind != "timeout" {
			t.Fatalf("%s: result=%+v err=%v", code, result, err)
		}
	}
	result, err := r.Execute(t.Context(), Request{Code: `return 42`, Timeout: time.Second}, noTools(t))
	if err != nil || string(result.Value) != "42" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRuntimeToolErrorsAndArguments(t *testing.T) {
	r := testRuntime(t)
	result, err := r.Execute(t.Context(), Request{
		Code:  `const out = []; try { await tools.read({fail: true}) } catch (e) { out.push(e.message) } try { await tools.read("x") } catch (e) { out.push(e.message) } out.push((await tools.read({id: 7})).id); return out`,
		Tools: []string{"read"}, MaxCalls: 3, Timeout: 2 * time.Second,
	}, func(_ context.Context, _ string, input json.RawMessage) (any, error) {
		if strings.Contains(string(input), "fail") {
			return nil, errors.New("tool exploded")
		}
		return input, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || string(result.Value) != `["tool exploded","tool arguments must be a bounded JSON object",7]` {
		t.Fatalf("result: %+v", result)
	}
}

func TestRuntimeStoreRoundTrip(t *testing.T) {
	r := testRuntime(t)
	first, err := r.Execute(t.Context(), Request{Code: `store("a", {n: 1}); store("b", [2]); return load("a").n`, Timeout: time.Second}, noTools(t))
	if err != nil || !first.OK || string(first.Value) != "1" || len(first.Store) != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := r.Execute(t.Context(), Request{Store: first.Store, Code: `store("b", undefined); return [load("a"), load("b")]`, Timeout: time.Second}, noTools(t))
	if err != nil || !second.OK || string(second.Value) != `[{"n":1},null]` || len(second.Store) != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	failed, err := r.Execute(t.Context(), Request{Store: first.Store, Code: `store("c", 1); throw new Error("no")`, Timeout: time.Second}, noTools(t))
	if err != nil || failed.OK || failed.Store != nil {
		t.Fatalf("failed run must not commit writes: %+v err=%v", failed, err)
	}
}
