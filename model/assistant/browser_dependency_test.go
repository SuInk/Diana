// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

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
