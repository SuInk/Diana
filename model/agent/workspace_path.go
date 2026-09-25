package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkspacePath 标记「路径写法不对」：绝对路径不在工作目录下，或者用 .. 跑出了工作目录。
// 文案直接写给模型看——以前是一句英文 required，模型看不出错在哪，只会换个姿势再错一遍。
var ErrWorkspacePath = errors.New("路径要写工作目录里的相对路径，比如 outputs/a.png；不知道文件在哪就先用 find_files 查")

// workspaceDirName 是工作目录自己的目录名（见 assistant.AgentWorkspaceDir）。模型在提示词和
// 工具结果里反复看到这个名字，很容易把它当成路径的第一段写进来。
const workspaceDirName = "workspace"

// NormalizeWorkspacePath 把模型写来的路径整理成工作目录下的相对路径，所有按工作目录读写
// 文件的入口都先过这一道，写法宽容的规则只维护这一份。
//
// 模型常见的写法和处理：
//   - 首尾空白、./ 前缀、Windows 反斜杠：直接规整掉；
//   - 工作目录的绝对路径（含 macOS 上 /var 与 /private/var 这种软链接两种写法）：去掉根；
//   - /workspace/x：沙盒容器里的习惯写法，按工作目录下的 x 处理；
//   - workspace/x：优先按字面找（工作目录里真有个叫 workspace 的子目录时照旧能用），
//     字面不存在而去掉前缀后存在，或者根本没有这个子目录时，才按 x 处理。
//
// 整理完仍是绝对路径、或者用 .. 跑出工作目录的，返回包了 ErrWorkspacePath 的错误。
// 这里只管写法，不解析软链接的最终落点：真正打开文件的地方仍然用 safePath 或
// os.OpenRoot 挡住指向工作目录外面的链接。空路径返回 "."。
func NormalizeWorkspacePath(root, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ".", nil
	}
	// Unix 上文件名里合法地带反斜杠的情况几乎不会出现在工作目录里，远不如模型照抄
	// Windows 路径常见，这里统一当分隔符。
	path = filepath.FromSlash(strings.ReplaceAll(path, `\`, "/"))
	if rel, ok := stripWorkspaceRoot(root, path); ok {
		return rel, nil
	}
	slash := filepath.ToSlash(filepath.Clean(path))
	switch {
	case slash == "/"+workspaceDirName:
		return ".", nil
	case strings.HasPrefix(slash, "/"+workspaceDirName+"/"):
		path = filepath.FromSlash(strings.TrimPrefix(slash, "/"+workspaceDirName+"/"))
	case filepath.IsAbs(path) || strings.HasPrefix(slash, "/") || looksLikeDrivePath(slash):
		return "", fmt.Errorf("%w（收到的 %s 不在工作目录下）", ErrWorkspacePath, strings.TrimSpace(raw))
	default:
		path = stripRelativeWorkspacePrefix(root, slash)
	}
	path = filepath.Clean(path)
	if path != "." && !filepath.IsLocal(path) {
		return "", fmt.Errorf("%w（%s 跑出了工作目录）", ErrWorkspacePath, strings.TrimSpace(raw))
	}
	return path, nil
}

// stripWorkspaceRoot 在 path 是工作目录（或其下某处）的绝对路径时返回相对部分。根和输入都各
// 比对原样与解析软链接后的两种形式：macOS 上 /var 本身是指向 /private/var 的链接，模型
// 从命令输出里抄来的可能是任一种。
func stripWorkspaceRoot(root, path string) (string, bool) {
	if !filepath.IsAbs(path) || strings.TrimSpace(root) == "" {
		return "", false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	roots := []string{absRoot}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil && resolved != absRoot {
		roots = append(roots, resolved)
	}
	inputs := []string{filepath.Clean(path)}
	if resolved, err := evalSymlinksAllowMissing(inputs[0]); err == nil && resolved != inputs[0] {
		inputs = append(inputs, resolved)
	}
	for _, input := range inputs {
		for _, base := range roots {
			rel, err := filepath.Rel(base, input)
			if err == nil && filepath.IsLocal(rel) {
				return rel, true
			}
		}
	}
	return "", false
}

// stripRelativeWorkspacePrefix 处理 workspace/x 这种多带了一层目录名的写法。slash 是已经
// Clean 过的正斜杠形式。字面路径存在就尊重字面，工作目录里真有 workspace 子目录时也
// 尊重字面（新建文件的场景没法靠「存在」判断），其余情况去掉这一层。
func stripRelativeWorkspacePrefix(root, slash string) string {
	var rest string
	switch {
	case slash == workspaceDirName:
		rest = "."
	case strings.HasPrefix(slash, workspaceDirName+"/"):
		rest = strings.TrimPrefix(slash, workspaceDirName+"/")
	default:
		return filepath.FromSlash(slash)
	}
	literal := filepath.FromSlash(slash)
	if workspaceEntryExists(root, literal) {
		return literal
	}
	if rest != "." && workspaceEntryExists(root, filepath.FromSlash(rest)) {
		return filepath.FromSlash(rest)
	}
	if workspaceEntryExists(root, workspaceDirName) {
		return literal
	}
	return filepath.FromSlash(rest)
}

func workspaceEntryExists(root, rel string) bool {
	if strings.TrimSpace(root) == "" || !filepath.IsLocal(rel) {
		return false
	}
	_, err := os.Lstat(filepath.Join(root, rel))
	return err == nil
}

// looksLikeDrivePath 认出 C:/Users/... 这种 Windows 盘符路径。在 Unix 上它不算绝对路径，
// 不拦的话会被当成工作目录下一个叫 C: 的子目录，报一个莫名其妙的「找不到」。
func looksLikeDrivePath(slash string) bool {
	if len(slash) < 3 || slash[1] != ':' || slash[2] != '/' {
		return false
	}
	c := slash[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// workspaceNotFound 给「文件不存在」补一句下一步该怎么做。只报 no such file 的话，模型
// 常会原样重试同一个路径；提示它用 find_files 按文件名查，通常一步就能找对。
// 仍然包着 fs.ErrNotExist，调用方的 errors.Is 判断不受影响。
func workspaceNotFound(rel string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fmt.Errorf("工作目录里没有 %s（%w）；不确定文件名或位置就用 find_files 按文件名查", strings.TrimSpace(rel), fs.ErrNotExist)
}
