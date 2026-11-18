// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeModeOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		input map[string]any
		want  int
	}{
		{map[string]any{}, 40_000},
		{map[string]any{"max_output_tokens": float64(20_000)}, 80_000},
		{map[string]any{"max_output_tokens": float64(10_000_000)}, 100_000},
		{map[string]any{"max_output_tokens": float64(-1)}, 40_000},
	} {
		if got := codeModeOutputBudget(tc.input); got != tc.want {
			t.Fatalf("budget(%v) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// 照 Pi：超出不报错，给首尾各一半，写明省略了多少；有 read_file 时完整输出存进工作区。
func TestCodeModeModelOutputKeepsHeadAndTail(t *testing.T) {
	output := "开头" + strings.Repeat("中", 96) + "结尾"
	plain := &Runner{registry: NewToolRegistry()}
	if got := plain.codeModeModelOutput(output, 200); got != output {
		t.Fatalf("output within budget changed: %q", got)
	}
	got := plain.codeModeModelOutput(output, 20)
	if !strings.Contains(got, "开头"+strings.Repeat("中", 8)+"\n…中间省略 80 字…\n"+strings.Repeat("中", 8)+"结尾") || !strings.Contains(got, "完整输出没有保存") {
		t.Fatalf("head/tail = %q", got)
	}

	root := t.TempDir()
	withFiles := &Runner{registry: NewToolRegistry(&ReadFileTool{root: root})}
	got = withFiles.codeModeModelOutput(output, 20)
	_, rest, found := strings.Cut(got, "完整输出已存到 ")
	if !found {
		t.Fatalf("no spill path: %q", got)
	}
	path, _, _ := strings.Cut(rest, "，")
	read, err := (&ReadFileTool{root: root}).Run(context.Background(), map[string]any{"path": path})
	if err != nil || !strings.Contains(read, strings.Repeat("中", 96)) || filepath.IsAbs(path) {
		t.Fatalf("spilled %q: %v %q", path, err, read)
	}
}
