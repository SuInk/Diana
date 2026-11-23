// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package codemode

import (
	"context"
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
)

// prelude.js is the PRELUDE_SOURCE of @earendil-works/pi-codemode 1.4.0,
// copied by scripts/codemode/build.mjs from an integrity-locked npm package (MIT).
//
//go:embed prelude.js
var preludeSource string

const vmMemoryLimitBytes = 32 * 1024 * 1024

var errBudget = errors.New("codemode: tool-call budget exceeded")

var compilePrelude = sync.OnceValues(func() (*moejs.Script, error) {
	return moejs.CompileScript("codemode-prelude.js", preludeSource)
})

// vm is one moejs runtime. Every method runs on the executing goroutine; only
// the interrupts arrive from other goroutines.
type vm struct {
	rt       *moejs.Runtime
	ctx      context.Context
	err      error
	deadline time.Time
	// interrupt is set when the host ends the run itself (the call budget).
	interrupt bool
	stops     []func() bool
	bridge    func(kind string, args []moejs.Value)
}

func newVM(ctx context.Context, deadline time.Time) *vm {
	// The prelude freezes the builtins itself and turns the commonly overridden
	// ones into accessors, as in Pi; that needs the runtime's own copy.
	rt := moejs.NewRuntime(moejs.Options{MutableIntrinsics: true, MemoryLimit: vmMemoryLimitBytes})
	v := &vm{rt: rt, ctx: ctx, deadline: deadline}
	timer := time.AfterFunc(time.Until(deadline), func() { rt.Interrupt(context.DeadlineExceeded) })
	v.stops = []func() bool{timer.Stop, context.AfterFunc(ctx, func() { rt.Interrupt(context.Cause(ctx)) })}
	return v
}

func (v *vm) close() {
	for _, stop := range v.stops {
		stop()
	}
}

func (v *vm) stop() {
	v.interrupt = true
	v.rt.Interrupt(errBudget)
}

func (v *vm) interrupted() bool {
	return v.interrupt || v.ctx.Err() != nil || !time.Now().Before(v.deadline)
}

// bridgeFunction is the prelude's only way out of the sandbox.
func (v *vm) bridgeFunction() moejs.Value {
	return v.rt.Function("bridge", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if v.bridge != nil && len(args) > 0 {
			v.bridge(v.toString(args[0]), args[1:])
		}
		return moejs.Undefined(), nil
	})
}

// call records the first failure and turns later calls into no-ops, so callers
// check v.err once per step instead of after every call.
func (v *vm) call(fn, this moejs.Value, args ...moejs.Value) moejs.Value {
	if v.err != nil {
		return moejs.Undefined()
	}
	value, err := v.rt.CallFunction(fn, this, args...)
	if err != nil {
		v.err = err
		return moejs.Undefined()
	}
	return value
}

func (v *vm) get(object moejs.Value, name string) moejs.Value {
	if v.err != nil {
		return moejs.Undefined()
	}
	value, err := v.rt.Get(object, name)
	if err != nil {
		v.err = err
	}
	return value
}

func (v *vm) toString(value moejs.Value) string {
	if v.err != nil {
		return ""
	}
	text, err := v.rt.Realm().ToString(value)
	if err != nil {
		v.err = err
		return ""
	}
	return text.GoString()
}
