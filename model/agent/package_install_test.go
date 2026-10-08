// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
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
