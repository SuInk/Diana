// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package safego

import (
	"os"
	"os/exec"
	"testing"

	"github.com/SuInk/diana/internal/safego/safegotest"
)

func TestRecoverRecoversWhenDeferredDirectly(t *testing.T) {
	safegotest.AssertRecovers(t, Recover, "probe", "probe")
}

func TestReportRecoversInsidePackageWrapper(t *testing.T) {
	wrapper := func(component string) { Report("pkg."+component, recover()) }
	safegotest.AssertRecovers(t, wrapper, "probe", "pkg.probe")
}

// The old per-package helpers called Recover from inside another deferred
// function. recover() then runs one frame too deep, returns nil, and the panic
// kills the process. Run that shape in a child process to pin the root cause.
func TestRecoverInsideWrapperDoesNotRecover(t *testing.T) {
	if os.Getenv("SAFEGO_WRAPPED_RECOVER_CHILD") == "1" {
		wrapper := func(component string) { Recover("pkg." + component) }
		func() {
			defer wrapper("probe")
			panic("wrapped recover probe")
		}()
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRecoverInsideWrapperDoesNotRecover$")
	command.Env = append(os.Environ(), "SAFEGO_WRAPPED_RECOVER_CHILD=1")
	if err := command.Run(); err == nil {
		t.Fatal("wrapped Recover unexpectedly stopped the panic; revisit the Report contract")
	}
}
