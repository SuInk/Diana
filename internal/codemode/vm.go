// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package codemode

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// quickjs.wasm is QuickJS-NG from quickjs-wasi 3.6.2 and prelude.js is the
// PRELUDE_SOURCE of @earendil-works/pi-codemode 1.4.0, both copied by
// scripts/codemode/build.mjs from integrity-locked npm packages (MIT).
var (
	//go:embed quickjs.wasm
	quickjsWASM []byte
	//go:embed prelude.js
	preludeSource string
)

const (
	vmMemoryLimitBytes = 32 * 1024 * 1024
	// QuickJS throws a catchable RangeError past this depth instead of trapping.
	vmMaxStackBytes = 512 * 1024
	// A hard cap on linear memory above the allocator limit: malloc bookkeeping
	// and the wasm stack live outside QuickJS's own accounting.
	vmMemoryLimitPages = 1024
	// Promise jobs run between two interrupt polls while draining the queue.
	vmJobsPerPoll = 50
)

type engine struct {
	runtime wazero.Runtime
	module  wazero.CompiledModule
}

type vmKey struct{}

var loadEngine = sync.OnceValues(func() (*engine, error) {
	ctx := context.Background()
	// CloseOnContextDone backs up the interrupt handler: a wasm loop that never
	// reaches QuickJS's opcode counter still stops when the execution ends.
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(vmMemoryLimitPages).WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	i32 := api.ValueTypeI32
	host := func(fn func(*vm, []uint64) uint64, params, results int) api.GoModuleFunction {
		return api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) {
			v, _ := ctx.Value(vmKey{}).(*vm)
			var out uint64
			if v != nil {
				out = fn(v, stack[:params])
			}
			if results > 0 {
				stack[0] = out
			}
		})
	}
	types := func(n int) []api.ValueType {
		out := make([]api.ValueType, n)
		for i := range out {
			out[i] = i32
		}
		return out
	}
	builder := runtime.NewHostModuleBuilder("env")
	for name, def := range map[string]struct {
		fn              func(*vm, []uint64) uint64
		params, results int
	}{
		"host_call":                {(*vm).hostCall, 5, 1},
		"host_interrupt":           {func(v *vm, _ []uint64) uint64 { return boolWord(v.interrupted()) }, 0, 1},
		"host_promise_rejection":   {(*vm).hostPromiseRejection, 3, 0},
		"host_module_normalize":    {func(*vm, []uint64) uint64 { return 0 }, 2, 1},
		"host_module_load":         {func(*vm, []uint64) uint64 { return 0 }, 2, 1},
		"host_get_timezone_offset": {hostTimezoneOffset, 2, 1},
	} {
		builder = builder.NewFunctionBuilder().WithGoModuleFunction(host(def.fn, def.params, def.results), types(def.params), types(def.results)).Export(name)
	}
	if _, err := builder.Instantiate(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	module, err := runtime.CompileModule(ctx, quickjsWASM)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	return &engine{runtime: runtime, module: module}, nil
})

// vm is one QuickJS instance. Every method runs on the executing goroutine:
// wasm memory is not safe for concurrent use.
type vm struct {
	ctx       context.Context
	mod       api.Module
	mem       api.Memory
	fns       map[string]api.Function
	err       error
	undefined uint32
	deadline  time.Time
	// interrupt is set by the executor when the run must end now.
	interrupt bool
	bridge    func(kind string, args []uint32)
}

func newVM(ctx context.Context, deadline time.Time) (*vm, error) {
	e, err := loadEngine()
	if err != nil {
		return nil, err
	}
	v := &vm{deadline: deadline, fns: map[string]api.Function{}}
	v.ctx = context.WithValue(ctx, vmKey{}, v)
	// No filesystem, environment, arguments or stdio: QuickJS only needs clocks
	// and randomness, and its diagnostics on fd 1/2 are discarded.
	config := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize").
		WithSysWalltime().WithSysNanotime().WithRandSource(rand.Reader).
		WithStdout(io.Discard).WithStderr(io.Discard)
	v.mod, err = e.runtime.InstantiateModule(v.ctx, e.module, config)
	if err != nil {
		return nil, err
	}
	v.mem = v.mod.Memory()
	if v.call("qjs_init") != 0 {
		v.close()
		return nil, errors.New("codemode: QuickJS initialization failed")
	}
	v.call("qjs_set_memory_limit", vmMemoryLimitBytes)
	v.call("qjs_set_max_stack_size", vmMaxStackBytes)
	v.call("qjs_set_interrupt_handler", 1)
	v.undefined = v.call("qjs_get_undefined")
	if v.err != nil {
		v.close()
		return nil, v.err
	}
	return v, nil
}

func (v *vm) close() {
	if v.mod != nil {
		_ = v.mod.Close(context.Background())
	}
}

func (v *vm) interrupted() bool {
	return v.interrupt || v.ctx.Err() != nil || !time.Now().Before(v.deadline)
}

// call records the first failure and turns later calls into no-ops, so callers
// check v.err once per step instead of after every export.
func (v *vm) call(name string, params ...uint64) uint32 {
	return uint32(v.callWord(name, params...))
}

func (v *vm) callWord(name string, params ...uint64) uint64 {
	if v.err != nil {
		return 0
	}
	fn := v.fns[name]
	if fn == nil {
		if fn = v.mod.ExportedFunction(name); fn == nil {
			v.err = fmt.Errorf("codemode: QuickJS export %s is missing", name)
			return 0
		}
		v.fns[name] = fn
	}
	results, err := fn.Call(v.ctx, params...)
	if err != nil {
		v.err = err
		return 0
	}
	if len(results) == 0 {
		return 0
	}
	return results[0]
}

func (v *vm) toNumber(value uint32) float64 {
	return math.Float64frombits(v.callWord("qjs_get_float64", uint64(value)))
}

func (v *vm) free(value uint32) {
	if value != 0 {
		v.call("qjs_free_value", uint64(value))
	}
}

func (v *vm) writeBytes(data string) (uint32, uint32) {
	ptr := v.call("wasm_malloc", uint64(len(data)+1))
	if v.err == nil && ptr == 0 {
		v.err = errors.New("codemode: wasm_malloc failed")
	}
	if v.err != nil {
		return 0, 0
	}
	if !v.mem.WriteString(ptr, data) || !v.mem.WriteByte(ptr+uint32(len(data)), 0) {
		v.err = errors.New("codemode: write outside wasm memory")
		return 0, 0
	}
	return ptr, uint32(len(data))
}

func (v *vm) newString(text string) uint32 {
	ptr, n := v.writeBytes(text)
	if v.err != nil {
		return 0
	}
	value := v.call("qjs_new_string", uint64(ptr), uint64(n))
	v.call("wasm_free", uint64(ptr))
	return value
}

func (v *vm) newNumber(n float64) uint32 {
	return v.call("qjs_new_number", math.Float64bits(n))
}

func (v *vm) bool(value bool) uint32 {
	if value {
		return v.call("qjs_get_true")
	}
	return v.call("qjs_get_false")
}

func (v *vm) isUndefined(value uint32) bool {
	return v.call("qjs_is_undefined", uint64(value)) != 0
}

func (v *vm) toString(value uint32) string {
	lenPtr := v.call("wasm_malloc", 4)
	if v.err != nil || lenPtr == 0 {
		return ""
	}
	defer v.call("wasm_free", uint64(lenPtr))
	cstr := v.call("qjs_get_string_len", uint64(value), uint64(lenPtr))
	if v.err != nil || cstr == 0 {
		return ""
	}
	defer v.call("qjs_free_cstring", uint64(cstr))
	n, _ := v.mem.ReadUint32Le(lenPtr)
	data, ok := v.mem.Read(cstr, n)
	if !ok {
		v.err = errors.New("codemode: read outside wasm memory")
		return ""
	}
	return string(data)
}

func (v *vm) newHostFunction(name string) uint32 {
	ptr, n := v.writeBytes(name)
	if v.err != nil {
		return 0
	}
	value := v.call("qjs_new_host_function", uint64(ptr), uint64(n), 0)
	v.call("wasm_free", uint64(ptr))
	return value
}

func (v *vm) getProp(object uint32, name string) uint32 {
	ptr, _ := v.writeBytes(name)
	if v.err != nil {
		return 0
	}
	value := v.call("qjs_get_prop_string", uint64(object), uint64(ptr))
	v.call("wasm_free", uint64(ptr))
	return value
}

// eval returns the value or, when the code throws, 0 and the exception.
func (v *vm) eval(code, filename string) (uint32, uint32) {
	codePtr, n := v.writeBytes(code)
	namePtr, _ := v.writeBytes(filename)
	value := v.call("qjs_eval", uint64(codePtr), uint64(n), uint64(namePtr), 0)
	v.call("wasm_free", uint64(codePtr))
	v.call("wasm_free", uint64(namePtr))
	return v.checked(value)
}

// callFunction passes argument handles without taking ownership of them.
func (v *vm) callFunction(fn, this uint32, args ...uint32) (uint32, uint32) {
	var argv uint32
	if len(args) > 0 {
		argv = v.call("wasm_malloc", uint64(4*len(args)))
		if v.err != nil {
			return 0, 0
		}
		for i, arg := range args {
			v.mem.WriteUint32Le(argv+uint32(4*i), arg)
		}
	}
	value := v.call("qjs_call", uint64(fn), uint64(this), uint64(len(args)), uint64(argv))
	if argv != 0 {
		v.call("wasm_free", uint64(argv))
	}
	return v.checked(value)
}

func (v *vm) checked(value uint32) (uint32, uint32) {
	if v.err != nil || v.call("qjs_is_exception", uint64(value)) == 0 {
		return value, 0
	}
	v.free(value)
	return 0, v.call("qjs_get_exception")
}

// drain runs queued promise jobs one at a time so long chains of tiny jobs
// (while (true) await null) still reach the interrupt check.
func (v *vm) drain() error {
	for jobs := 1; v.err == nil && v.call("qjs_is_job_pending") != 0; jobs++ {
		if int32(v.call("qjs_execute_pending_job")) < 0 {
			return errors.New("promise job failed")
		}
		if jobs%vmJobsPerPoll == 0 && v.interrupted() {
			return errors.New("execution interrupted")
		}
	}
	return v.err
}

// hostCall is the C trampoline's entry for host functions. The prelude's bridge
// is the only one; argument handles are borrowed and freed by the caller.
func (v *vm) hostCall(params []uint64) uint64 {
	nameData, _ := v.mem.Read(uint32(params[0]), uint32(params[1]))
	argc, argv := uint32(params[3]), uint32(params[4])
	if string(nameData) != "bridge" || v.bridge == nil || argc == 0 {
		return uint64(v.call("qjs_dup_value", uint64(v.undefined)))
	}
	args := make([]uint32, argc)
	for i := range args {
		args[i], _ = v.mem.ReadUint32Le(argv + uint32(4*i))
	}
	v.bridge(v.toString(args[0]), args[1:])
	return uint64(v.call("qjs_dup_value", uint64(v.undefined)))
}

func (v *vm) hostPromiseRejection(params []uint64) uint64 {
	v.free(uint32(params[0]))
	v.free(uint32(params[1]))
	return 0
}

func hostTimezoneOffset(_ *vm, params []uint64) uint64 {
	seconds := int64(uint32(params[0]))<<32 | int64(uint32(params[1]))
	_, offset := time.Unix(seconds, 0).Zone()
	return uint64(uint32(int32(offset)))
}

func boolWord(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
