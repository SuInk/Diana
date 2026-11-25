// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const boxMissingFFmpeg = `{"exit_code": 254, "output": "/work/downloads/laowu_video.mp4: No such file or directory"}`

func writeHintWorkspaceFile(t *testing.T, rel string) {
	t.Helper()
	full := filepath.Join(AgentWorkspaceDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 11-26「保存gif」：视频存在工作目录 downloads/，ffmpeg 读 /work/downloads/… 失败。
// 提示要点名这个文件、给出可照抄的 inputs 和 /work/inbox 路径。
func TestBoxWorkspacePathHintNamesSavedFile(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	writeHintWorkspaceFile(t, "downloads/laowu_video.mp4")
	command := `ffmpeg -y -i /work/downloads/laowu_video.mp4 -vf "fps=15" -loop 0 /work/downloads/laowu.gif`
	hint := r.boxWorkspacePathHint(event, true, command, boxMissingFFmpeg)
	for _, want := range []string{`inputs 写 ["downloads/laowu_video.mp4"]`, "/work/inbox/laowu_video.mp4", "不在容器里"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing %q: %s", want, hint)
		}
	}
	// 输出文件 laowu.gif 还不存在，不该被当成要传进去的输入。
	if strings.Contains(hint, "laowu.gif\"") {
		t.Fatalf("hint lists the output as an input: %s", hint)
	}
}

// 路径像工作目录、但工作目录里也没有这个文件时，给通用说明，不编造具体路径。
func TestBoxWorkspacePathHintGenericWhenFileUnknown(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	hint := r.boxWorkspacePathHint(event, true, "ls ./outputs/nothing.png", "ls: cannot access './outputs/nothing.png': No such file or directory")
	if !strings.Contains(hint, "inputs") || !strings.Contains(hint, "find_files") || strings.Contains(hint, "nothing.png") {
		t.Fatalf("generic hint = %s", hint)
	}
}

// 不是找不到文件、或命令里没有工作目录路径时不加提示。
func TestBoxWorkspacePathHintOnlyForMissingWorkspacePaths(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	writeHintWorkspaceFile(t, "downloads/a.mp4")
	if hint := r.boxWorkspacePathHint(event, true, "ffmpeg -i /work/downloads/a.mp4 out.gif", `{"exit_code": 1, "output": "Invalid data found when processing input"}`); hint != "" {
		t.Fatalf("non-missing failure got hint: %s", hint)
	}
	if hint := r.boxWorkspacePathHint(event, true, "cat /work/repo/README.md", boxMissingFFmpeg); hint != "" {
		t.Fatalf("non-workspace path got hint: %s", hint)
	}
}

// 普通群成员只在文件属于本群保存记录时被点名，别借提示探到别处有什么。
func TestBoxWorkspacePathHintRespectsGroupScope(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	writeHintWorkspaceFile(t, "downloads/private.mp4")
	hint := r.boxWorkspacePathHint(event, false, "ffmpeg -i /work/downloads/private.mp4 out.gif", boxMissingFFmpeg)
	if hint == "" || strings.Contains(hint, "private.mp4") {
		t.Fatalf("member hint leaked an ungranted file: %s", hint)
	}
	scope := r.workspaceGroupScope(event)
	rel := "downloads/groups/" + scope + "/mine.mp4"
	writeHintWorkspaceFile(t, rel)
	if err := r.grantWorkspaceGroupFile(event, rel, []byte("video")); err != nil {
		t.Fatal(err)
	}
	hint = r.boxWorkspacePathHint(event, false, "ffmpeg -i /work/"+rel+" out.gif", boxMissingFFmpeg)
	if !strings.Contains(hint, `"`+rel+`"`) {
		t.Fatalf("member hint should name own group file: %s", hint)
	}
}
