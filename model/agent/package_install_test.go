// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNPMPackages(t *testing.T) {
	got, err := ValidateNPMPackages([]string{" sharp ", "@types/node@20", "lodash@^4.17.0", "sharp"})
	if err != nil || strings.Join(got, ",") != "sharp,@types/node@20,lodash@^4.17.0" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"-g", "--prefix=/", "http://x/y.tgz", "git+https://x", "github:a/b", "file:x", "./x", "../x", "/x", "Sharp", "a/b", ""} {
		if _, err := ValidateNPMPackages([]string{bad}); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
	many := make([]string, MaxInstallPackages+1)
	for i := range many {
		many[i] = "p" + strings.Repeat("x", i+1)
	}
	if _, err := ValidateNPMPackages(many); err == nil {
		t.Fatal("超过上限未拒绝")
	}
}

func TestPackageInstallHonorsSandboxConfiguration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkDir: t.TempDir(), CommandSandbox: CommandSandboxRequire}
	if _, err := packageInstallCommand(context.Background(), cfg, executable, []string{"sharp"}, commandSandbox{}); err == nil {
		t.Fatal("require mode ran without a sandbox")
	}
	for _, network := range []bool{false, true} {
		cfg.CommandSandboxAllowNetwork = network
		called := false
		sandbox := commandSandbox{kind: "test", wrap: func(ctx context.Context, root string, allowNetwork bool, secrets []string, name string, args []string) *exec.Cmd {
			called = true
			if root != cfg.WorkDir || allowNetwork != network {
				t.Fatalf("sandbox config lost: %s %v", root, allowNetwork)
			}
			if !strings.Contains(strings.Join(args, " "), filepath.Join(cfg.WorkDir, ".npm-cache")) {
				t.Fatal("npm cache escaped workspace")
			}
			return exec.CommandContext(ctx, name, args...)
		}}
		if _, err := packageInstallCommand(context.Background(), cfg, executable, []string{"sharp"}, sandbox); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("sandbox wrapper bypassed")
		}
	}
}
