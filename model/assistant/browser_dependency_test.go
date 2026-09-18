// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"github.com/SuInk/diana/model/agent"
	"strings"
	"testing"
)

func TestRelationRenderDependenciesReportFontAndBrowserSeparately(t *testing.T) {
	browser := ResolverDependency{
		Name:      browserDependencyName,
		Available: true,
		Path:      "/test/chrome",
		Version:   "Chromium test",
	}
	deps := RelationRenderDependencies([]ResolverDependency{browser})
	if len(deps) != 2 {
		t.Fatalf("dependencies=%#v", deps)
	}
	if deps[0].Name != relationFontDependencyName {
		t.Fatalf("first dependency should report the font path: %#v", deps[0])
	}
	if deps[1].Name != browserDependencyName || !deps[1].Available || deps[1].Path != browser.Path {
		t.Fatalf("browser probe was not preserved: %#v", deps[1])
	}
}

func TestBrowserDependencyUsesSystemInstaller(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "apk" {
			return "/sbin/apk", nil
		}
		return "", fmt.Errorf("missing")
	}
	deps := browserDependenciesFromStatus(agent.HeadlessBrowserStatus{Detail: "missing browser"}, "linux", lookup)
	if len(deps) != 1 || deps[0].Available || !deps[0].Installable || deps[0].Installer != "apk" {
		t.Fatalf("wrong installer: %#v", deps)
	}
	deps = browserDependenciesFromStatus(agent.HeadlessBrowserStatus{Available: true, Path: "/usr/bin/chromium", Version: "Chromium 142"}, "linux", lookup)
	if !deps[0].Available || deps[0].Installable || deps[0].Path != "/usr/bin/chromium" {
		t.Fatalf("existing browser not reused: %#v", deps)
	}
	deps = browserDependenciesFromStatus(agent.HeadlessBrowserStatus{Detail: "screenshot failed"}, "linux", func(string) (string, error) { return "", fmt.Errorf("missing") })
	if deps[0].Installable || !strings.Contains(deps[0].Detail, "手动安装") || !strings.Contains(deps[0].Detail, "screenshot failed") {
		t.Fatalf("missing installer: %#v", deps)
	}
}
