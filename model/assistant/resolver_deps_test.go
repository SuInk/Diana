// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestResolverDependenciesProbes(t *testing.T) {
	deps := ResolverDependencies()
	if len(deps) != len(resolverDependencySpecs) {
		t.Fatalf("期望探测 %d 个依赖，实际 %d", len(resolverDependencySpecs), len(deps))
	}
	for _, dep := range deps {
		if dep.Name == "" || dep.Purpose == "" {
			t.Fatalf("依赖信息不完整：%+v", dep)
		}
		if dep.Available && dep.Path == "" {
			t.Fatalf("%s 可用但没有路径", dep.Name)
		}
		t.Logf("%s available=%v version=%q", dep.Name, dep.Available, dep.Version)
	}
}

func TestResolverDependencyInstallPlanUsesHomebrewWhitelist(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "brew" {
			return "/opt/homebrew/bin/brew", nil
		}
		return "", fmt.Errorf("missing %s", name)
	}
	plan, err := resolverDependencyInstallPlan("yt-dlp", "darwin", lookPath)
	if err != nil {
		t.Fatalf("resolverDependencyInstallPlan() error = %v", err)
	}
	if plan.installer != "Homebrew" || len(plan.commands) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.commands[0].path != "/opt/homebrew/bin/brew" || !reflect.DeepEqual(plan.commands[0].args, []string{"install", "yt-dlp"}) {
		t.Fatalf("command = %#v", plan.commands[0])
	}
}

func TestResolverDependencyInstallPlanUsesAptNodePackage(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "apt-get" {
			return "/usr/bin/apt-get", nil
		}
		return "", fmt.Errorf("missing %s", name)
	}
	plan, err := resolverDependencyInstallPlan("node", "linux", lookPath)
	if err != nil {
		t.Fatalf("resolverDependencyInstallPlan() error = %v", err)
	}
	if plan.installer != "apt" || len(plan.commands) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if !reflect.DeepEqual(plan.commands[1].args, []string{"install", "-y", "nodejs"}) {
		t.Fatalf("install args = %#v", plan.commands[1].args)
	}
}

func TestResolverDependencyInstallPlanRejectsUnknownName(t *testing.T) {
	_, err := resolverDependencyInstallPlan("anything; rm -rf", "darwin", func(string) (string, error) {
		return "/opt/homebrew/bin/brew", nil
	})
	if !errors.Is(err, ErrUnknownResolverDependency) {
		t.Fatalf("error = %v, want ErrUnknownResolverDependency", err)
	}
}
