// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// run_command 找不到文件时，报错要说到点子上。
//
// 工作目录（save_to_workspace、生图存的文件）在宿主机上，容器的 /work 是另一个卷，
// 两边不互通，文件要靠 inputs 放进本会话工作区的 inbox/。11-26 群主 @ 机器人「保存gif」：视频
// 存进了 downloads/，模型照着这个路径跑 ffmpeg -i /work/downloads/…，连试 5 次都只拿到
// ffmpeg 的「No such file or directory」，在容器里 find 了一圈，最后以「被沙箱隔离挡住」
// 静默收尾。错误本身没错，但它没说为什么找不到、该怎么改，模型只能瞎猜。

// boxWorkspacePathPattern 认出命令里像工作目录分区的路径：/work/downloads/x，或者
// 以 /work 为当前目录写的 downloads/x、./downloads/x。
var boxWorkspacePathPattern = regexp.MustCompile(`(?:^|[\s"'=(])(?:/work/|\./)?((?:` +
	agent.WorkspaceDownloadsDir + `|` + agent.WorkspaceOutputsDir + `|` + agent.WorkspaceKeepDir + `|` + agent.WorkspaceTmpDir +
	`)/[^\s"'|;&<>()\\]+)`)

// boxMissingFileMarkers 是「文件不存在」的常见报错。只有命令确实因为找不到文件失败时
// 才补提示：容器里自己建了 /work/downloads 再出别的错，不该被说成路径问题。
var boxMissingFileMarkers = []string{"No such file or directory", "does not exist", "not found", "cannot find"}

// boxWorkspacePathHint 在 run_command 失败、报错是找不到文件、命令里又引用了工作目录
// 分区路径时，返回一句写给模型的纠正说明；不相干时返回空串。owner 为 false 时只在
// 文件属于本群保存记录时点名具体文件，别借提示探到别处有什么。
func (r *Runtime) boxWorkspacePathHint(event MessageEvent, owner bool, command, failure string) string {
	if !boxFailureIsMissingFile(failure) {
		return ""
	}
	matches := boxWorkspacePathPattern.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return ""
	}
	root := AgentWorkspaceDir()
	seen := map[string]bool{}
	var found []string
	for _, match := range matches {
		rel := strings.TrimRight(match[1], ".,:")
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if r.workspaceFileVisibleForHint(event, owner, root, rel) {
			found = append(found, rel)
		}
	}
	const separated = "工作目录（save_to_workspace、生图存的文件）在宿主机上，容器里的工作区是另一处，两边不互通，容器里没有这些文件。"
	if len(found) == 0 {
		return "提示：" + separated + "要在命令里处理工作目录的文件，把它的 path 写进 run_command 的 inputs，文件会放进当前目录的 inbox/<文件名>；不知道 path 就先用 find_files 查。"
	}
	var inputs, inbox []string
	for _, rel := range found {
		inputs = append(inputs, `"`+rel+`"`)
		inbox = append(inbox, path.Join("inbox", path.Base(rel)))
	}
	return "提示：" + strings.Join(found, "、") + " 在工作目录里，不在容器里。" + separated +
		"重跑时 inputs 写 [" + strings.Join(inputs, ", ") + "]，命令里改用 " + strings.Join(inbox, "、") +
		"；产物写在当前目录（例如 out.gif），发送时 send_file 的 path 写 /work/out.gif。"
}

func boxFailureIsMissingFile(failure string) bool {
	for _, marker := range boxMissingFileMarkers {
		if strings.Contains(failure, marker) {
			return true
		}
	}
	return false
}

// workspaceFileVisibleForHint 报告工作目录里有没有这个文件、且这位发言者看得到它。
func (r *Runtime) workspaceFileVisibleForHint(event MessageEvent, owner bool, root, raw string) bool {
	rel, err := agent.NormalizeWorkspacePath(root, raw)
	if err != nil || rel == "." {
		return false
	}
	info, err := os.Stat(filepath.Join(root, rel))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if owner {
		return true
	}
	_, err = r.workspaceGroupFileGrant(event, rel)
	return err == nil
}
