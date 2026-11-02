// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// 群成员的 run_command。主人那套沙盒是给主人设计的：读取几乎全放开、写入是整个共享
// 工作目录、不限资源——主人本来就能碰这些。成员不行，所以另起一套更紧的：
//
//   - 读取是白名单：系统库、解释器和成员自己的目录，家目录、工作区其他地方、别的群
//     都看不见；
//   - 写入只到这个成员在本群的目录；
//   - 永远断网，不受联网开关影响；
//   - 限 CPU、内存、进程数、单文件大小、目录总量、墙钟时长和输出长度；
//   - 沙盒探不通就拒绝，不像主人的 auto 模式那样退回裸跑。
//
// 程序也只放几样：成员要的是「跑段 Python/Node 生成个文件」，不是一台机器。

// MemberCommandLimits 是成员命令的资源上限。
type MemberCommandLimits struct {
	Wall        time.Duration
	CPU         time.Duration
	MemoryBytes int64
	Processes   int
	FileBytes   int64
	DirBytes    int64
	DirFiles    int
	OutputBytes int
	Retention   time.Duration
	ReportFiles int
}

// DefaultMemberCommandLimits 是成员命令的默认上限。进程数要给 bubblewrap 自己的两三个
// 进程留出余量；macOS 上直接禁止 fork，进程数恒为 1。
var DefaultMemberCommandLimits = MemberCommandLimits{
	Wall:        30 * time.Second,
	CPU:         10 * time.Second,
	MemoryBytes: 512 << 20,
	Processes:   16,
	FileBytes:   20 << 20,
	DirBytes:    50 << 20,
	DirFiles:    500,
	OutputBytes: 8 << 10,
	Retention:   24 * time.Hour,
	ReportFiles: 20,
}

// memberCommandPrograms 是成员能起的程序。只读的几样文本工具方便模型看看自己生成了
// 什么；它们在沙盒里同样只看得到白名单路径。
var memberCommandPrograms = []string{"python3", "python", "node", "ls", "cat", "head", "tail", "wc", "file"}

// MemberCommandPrograms 返回成员可用的程序名，给工具描述用。
func MemberCommandPrograms() []string { return append([]string(nil), memberCommandPrograms...) }

// MemberCommandRequest 是一次成员命令。
type MemberCommandRequest struct {
	// Dir 是这个成员在本群的目录，绝对路径；不存在时自动建出来。
	Dir     string
	Command string
	Args    []string
	Timeout time.Duration
	Limits  MemberCommandLimits
}

// MemberCommandFile 是这次运行新增或改过的文件。
type MemberCommandFile struct {
	// Name 是相对 Dir 的路径，用 / 分隔。
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Data 是还持有目录锁时读出的内容，调用方拿它登记访问凭据。事后再按路径读不
	// 安全：同一成员并发的下一条命令可以把中间的目录换成指向别处的链接。
	Data []byte `json:"-"`
}

// MemberCommandResult 是成员命令的执行结果。
type MemberCommandResult struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	ExitCode   int      `json:"exit_code"`
	TimedOut   bool     `json:"timed_out"`
	DurationMS int64    `json:"duration_ms"`
	// LimitExceeded 说明被看门狗杀掉的原因：memory / processes / disk，没超限时为空。
	LimitExceeded string              `json:"limit_exceeded,omitempty"`
	Output        string              `json:"output"`
	Truncated     bool                `json:"truncated"`
	Sandbox       string              `json:"sandbox"`
	Files         []MemberCommandFile `json:"files"`
	// MoreFiles 是超过 ReportFiles 没列出来的文件数。
	MoreFiles int `json:"more_files,omitempty"`
}

// memberSandbox 是成员沙盒的一种实现。
type memberSandbox struct {
	kind string
	// build 包装命令；spec.dir 已经是解析过符号链接的真实路径。
	build func(ctx context.Context, spec memberSandboxSpec) *exec.Cmd
	// view 是沙盒里看到的成员目录路径：bubblewrap 挂到 /work，sandbox-exec 原地不动。
	view func(dir string) string
}

func (s memberSandbox) available() bool { return s.build != nil }

// memberSandboxSpec 是交给沙盒实现的一次运行参数。
type memberSandboxSpec struct {
	dir     string
	command string
	args    []string
	// readRoots 是在基础白名单之外还要放开读取的目录，目前只有解释器的安装前缀。
	readRoots []string
	limits    MemberCommandLimits
}

var (
	memberSandboxOnce     sync.Once
	memberSandboxDetected memberSandbox
	memberSandboxReason   string
	memberSandboxProbe    = runMemberSandboxProbe
	memberSandboxFn       = memberSandboxCandidate
)

// MemberCommandAvailable 报告成员沙盒能不能用。探测只做一次：与主人沙盒一样，这是
// 进程级的环境事实。
func MemberCommandAvailable() (kind string, reason string) {
	sandbox := detectMemberSandbox()
	if sandbox.available() {
		return sandbox.kind, ""
	}
	return "", memberSandboxReason
}

func detectMemberSandbox() memberSandbox {
	memberSandboxOnce.Do(func() {
		memberSandboxDetected, memberSandboxReason = probeMemberSandbox()
	})
	return memberSandboxDetected
}

func probeMemberSandbox() (memberSandbox, string) {
	candidate, reason := memberSandboxFn()
	if !candidate.available() {
		return memberSandbox{}, reason
	}
	if err := memberSandboxProbe(candidate); err != nil {
		return memberSandbox{}, fmt.Sprintf("%s 已安装但成员沙盒试运行失败（%v）", candidate.kind, err)
	}
	return candidate, ""
}

func memberSandboxCandidate() (memberSandbox, string) {
	switch runtime.GOOS {
	case "darwin":
		path, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return memberSandbox{}, "这台机器上找不到 sandbox-exec"
		}
		return memberSandbox{kind: "sandbox-exec", build: memberSandboxExecCommand(path), view: func(dir string) string { return dir }}, ""
	case "linux", "android":
		path, err := exec.LookPath("bwrap")
		if err != nil {
			return memberSandbox{}, "这台机器上没有安装 bubblewrap（bwrap）"
		}
		return memberSandbox{kind: "bubblewrap", build: memberBubblewrapCommand(path), view: func(string) string { return memberBubblewrapWorkDir }}, ""
	default:
		return memberSandbox{}, fmt.Sprintf("%s 上没有成员可用的沙盒实现", runtime.GOOS)
	}
}

// runMemberSandboxProbe 在临时目录里按成员规则跑一次 true：白名单、ulimit 和命名空间
// 任何一样不通，这里就会失败。
func runMemberSandboxProbe(sandbox memberSandbox) error {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	root, err := os.MkdirTemp("", "diana-member-probe-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	command, err := lookupMemberProgram("true")
	if err != nil {
		return err
	}
	cmd := sandbox.build(ctx, memberSandboxSpec{dir: root, command: command, limits: DefaultMemberCommandLimits})
	cmd.Dir = root
	cmd.Env = memberCommandEnv(sandbox.view(root), command)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// memberDirLocks 让同一个成员目录同一时间只跑一条命令：文件快照和授权都假设运行期间
// 没有别人改目录。
var memberDirLocks sync.Map

func lockMemberDir(dir string) func() {
	value, _ := memberDirLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// RunMemberCommand 在成员沙盒里跑一条命令。沙盒不可用时直接报错，不退回裸跑。
func RunMemberCommand(ctx context.Context, req MemberCommandRequest) (MemberCommandResult, error) {
	sandbox := detectMemberSandbox()
	if !sandbox.available() {
		return MemberCommandResult{}, fmt.Errorf("成员命令沙盒不可用，不能运行：%s", memberSandboxReason)
	}
	return runMemberCommand(ctx, sandbox, req)
}

func runMemberCommand(ctx context.Context, sandbox memberSandbox, req MemberCommandRequest) (MemberCommandResult, error) {
	limits := req.Limits
	if limits.Wall <= 0 {
		limits = DefaultMemberCommandLimits
	}
	name := strings.TrimSpace(req.Command)
	if !memberProgramAllowed(name) {
		return MemberCommandResult{}, fmt.Errorf("群成员只能运行这些程序：%s", strings.Join(memberCommandPrograms, "、"))
	}
	command, err := lookupMemberProgram(name)
	if err != nil {
		return MemberCommandResult{}, fmt.Errorf("这台机器上没有 %s", name)
	}
	if !filepath.IsAbs(req.Dir) {
		return MemberCommandResult{}, errors.New("member command dir must be absolute")
	}
	if err := os.MkdirAll(req.Dir, 0o700); err != nil {
		return MemberCommandResult{}, err
	}
	dir, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return MemberCommandResult{}, err
	}
	unlock := lockMemberDir(dir)
	defer unlock()

	pruneMemberDir(dir, limits.Retention, time.Now())
	usage := memberDirUsage(dir, limits)
	if usage.bytes > limits.DirBytes || usage.files > limits.DirFiles {
		return MemberCommandResult{}, fmt.Errorf("个人目录已用 %d 字节、%d 个文件，超过上限（%d 字节、%d 个文件）；文件在 %s 后自动清理", usage.bytes, usage.files, limits.DirBytes, limits.DirFiles, limits.Retention)
	}
	tmp := filepath.Join(dir, memberTmpDirName)
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return MemberCommandResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	before := snapshotMemberDir(dir)

	timeout := req.Timeout
	if timeout <= 0 || timeout > limits.Wall {
		timeout = limits.Wall
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := sandbox.build(runCtx, memberSandboxSpec{
		dir:       dir,
		command:   command,
		args:      req.Args,
		readRoots: memberInterpreterRoots(command),
		limits:    limits,
	})
	cmd.Dir = dir
	cmd.Env = memberCommandEnv(sandbox.view(dir), command)
	output := &cappedBuffer{limit: limits.OutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return MemberCommandResult{}, err
	}
	exceeded := make(chan string, 1)
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		defer recoverGoroutinePanic("member command watchdog")
		watchMemberCommand(cmd.Process.Pid, dir, limits, stopWatch, func(reason string) {
			exceeded <- reason
			cancel()
		})
	}()
	waitErr := cmd.Wait()
	close(stopWatch)
	<-watchDone
	duration := time.Since(start)

	result := MemberCommandResult{
		Command:    name,
		Args:       append([]string{}, req.Args...),
		DurationMS: duration.Milliseconds(),
		Output:     strings.ToValidUTF8(output.String(), ""),
		Truncated:  output.truncated,
		Sandbox:    sandbox.kind,
	}
	select {
	case reason := <-exceeded:
		result.LimitExceeded = reason
	default:
	}
	result.TimedOut = result.LimitExceeded == "" && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) && runCtx.Err() == nil {
			return MemberCommandResult{}, waitErr
		}
		result.ExitCode = -1
		if exitErr != nil && exitErr.ExitCode() >= 0 {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	result.Files, result.MoreFiles = changedMemberFiles(dir, before, limits)
	return result, nil
}

func memberProgramAllowed(name string) bool {
	for _, allowed := range memberCommandPrograms {
		if name == allowed {
			return true
		}
	}
	return name == "true"
}

// lookupMemberProgram 找程序的绝对路径。macOS 的 /usr/bin/python3 只是 Xcode 的
// 转发壳，要再起一个子进程才能跑到真解释器——成员沙盒禁 fork，它必然失败，所以
// 先找 Homebrew 的，再退回 PATH 里第一个不是转发壳的。
func lookupMemberProgram(name string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("command must be a binary name, not a path")
	}
	dirs := filepath.SplitList(executionSearchPath())
	if runtime.GOOS == "darwin" {
		dirs = append([]string{"/opt/homebrew/bin", "/usr/local/bin"}, dirs...)
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		if runtime.GOOS == "darwin" && filepath.Dir(path) == "/usr/bin" && strings.HasPrefix(name, "python") {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// memberInterpreterRoots 推出解释器的安装前缀（真实路径的 bin 的上一级），装在
// pyenv、nvm 这类非系统位置时靠它读到标准库。前缀太浅（/、/usr 这一级）或者盖住了
// 家目录、工作区时不放：那等于把白名单拆了。
func memberInterpreterRoots(command string) []string {
	real, err := filepath.EvalSymlinks(command)
	if err != nil {
		return nil
	}
	prefix := filepath.Dir(filepath.Dir(real))
	if !memberPrefixAcceptable(prefix) {
		return nil
	}
	for _, base := range memberBaseReadRoots() {
		if pathWithin(base, prefix) {
			return nil
		}
	}
	return []string{prefix}
}

func memberPrefixAcceptable(prefix string) bool {
	clean := filepath.Clean(prefix)
	if strings.Count(strings.Trim(filepath.ToSlash(clean), "/"), "/") < 1 {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && pathWithin(clean, home) {
		return false
	}
	if wd, err := os.Getwd(); err == nil && pathWithin(clean, wd) {
		return false
	}
	return true
}

// pathWithin 判断 child 是否等于 parent 或在它下面。
func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// memberCommandEnv 是成员命令的全部环境变量。不继承 Diana 的进程环境：里面有令牌，
// 成员不需要任何一个。
func memberCommandEnv(dir, command string) []string {
	path := []string{filepath.Dir(command), "/usr/local/bin", "/usr/bin", "/bin"}
	if runtime.GOOS == "darwin" {
		path = append(path, "/opt/homebrew/bin")
	}
	return []string{
		"PATH=" + strings.Join(path, ":"),
		"HOME=" + dir,
		"TMPDIR=" + filepath.Join(dir, memberTmpDirName),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONNOUSERSITE=1",
	}
}

// memberTmpDirName 是成员目录里的临时目录，每次运行前后清空，里面的文件不算产物。
const memberTmpDirName = ".tmp"

// memberShellArgs 用 /bin/sh 先设好 rlimit 再 exec 目标程序。CPU 和单文件大小两个
// 平台都认；内存不用 RLIMIT_AS——V8 一启动就要预留几十 GB 地址空间，限了 node 起不来，
// 内存交给看门狗按 RSS 管。
func memberShellArgs(spec memberSandboxSpec) []string {
	cpu := int64(spec.limits.CPU / time.Second)
	if cpu <= 0 {
		cpu = 1
	}
	blocks := spec.limits.FileBytes / 512
	if blocks <= 0 {
		blocks = 1
	}
	script := fmt.Sprintf(`ulimit -t %d && ulimit -f %d && exec "$@"`, cpu, blocks)
	return append([]string{"/bin/sh", "-c", script, "sh", spec.command}, spec.args...)
}

// watchMemberCommand 每隔一小段时间看一眼进程树和目录：内存、进程数、磁盘任何一项
// 超了就杀掉整条命令。rlimit 管不住的几项都靠它。
func watchMemberCommand(pid int, dir string, limits MemberCommandLimits, stop <-chan struct{}, kill func(string)) {
	ticker := time.NewTicker(memberWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		if reason := memberLimitReason(pid, dir, limits); reason != "" {
			kill(reason)
			return
		}
	}
}

var memberWatchInterval = 200 * time.Millisecond

func memberLimitReason(pid int, dir string, limits MemberCommandLimits) string {
	if count, rss, err := processTreeStats(pid); err == nil {
		if limits.Processes > 0 && count > limits.Processes {
			return "processes"
		}
		if limits.MemoryBytes > 0 && rss > limits.MemoryBytes {
			return "memory"
		}
	}
	usage := memberDirUsage(dir, limits)
	if usage.bytes > limits.DirBytes || usage.files > limits.DirFiles {
		return "disk"
	}
	return ""
}

type memberUsage struct {
	bytes int64
	files int
}

// memberDirUsage 统计目录占用，超过上限就停，不在一个塞满小文件的目录里走到底。
func memberDirUsage(dir string, limits MemberCommandLimits) memberUsage {
	var usage memberUsage
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		usage.files++
		if info, err := entry.Info(); err == nil && entry.Type().IsRegular() {
			usage.bytes += info.Size()
		}
		if usage.bytes > limits.DirBytes || usage.files > limits.DirFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return usage
}

// pruneMemberDir 删掉过了保留期的文件和随之空掉的目录。
func pruneMemberDir(dir string, retention time.Duration, now time.Time) {
	if retention <= 0 {
		return
	}
	var dirs []string
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > retention {
			_ = os.Remove(path)
		}
		return nil
	})
	// 由深到浅删空目录，非空的 Remove 会失败，正好留下。
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, path := range dirs {
		_ = os.Remove(path)
	}
}

type memberFileStamp struct {
	size    int64
	modTime time.Time
}

func snapshotMemberDir(dir string) map[string]memberFileStamp {
	stamps := map[string]memberFileStamp{}
	walkMemberFiles(dir, func(name string, info fs.FileInfo) {
		stamps[name] = memberFileStamp{size: info.Size(), modTime: info.ModTime()}
	})
	return stamps
}

// changedMemberFiles 列出这次运行新增或改过的普通文件。符号链接一律不算：指向白名单
// 外面的链接在宿主机上解析出来就是别处的文件，不能拿它去发凭据。
func changedMemberFiles(dir string, before map[string]memberFileStamp, limits MemberCommandLimits) ([]MemberCommandFile, int) {
	files := []MemberCommandFile{}
	walkMemberFiles(dir, func(name string, info fs.FileInfo) {
		old, ok := before[name]
		if ok && old.size == info.Size() && old.modTime.Equal(info.ModTime()) {
			return
		}
		files = append(files, MemberCommandFile{Name: name, Size: info.Size()})
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	more := 0
	if limits.ReportFiles > 0 && len(files) > limits.ReportFiles {
		more = len(files) - limits.ReportFiles
		files = files[:limits.ReportFiles]
	}
	return readMemberFiles(dir, files, limits.FileBytes), more
}

// readMemberFiles 在成员目录这个根里读出产物：os.Root 不跟随指出根的链接，读不到
// 的（被删、超限、是链接）直接丢掉。
func readMemberFiles(dir string, files []MemberCommandFile, limit int64) []MemberCommandFile {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return []MemberCommandFile{}
	}
	defer root.Close()
	out := files[:0]
	for _, file := range files {
		name := filepath.FromSlash(file.Name)
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || (limit > 0 && info.Size() > limit) {
			continue
		}
		f, err := root.Open(name)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
		_ = f.Close()
		if err != nil || int64(len(data)) != info.Size() {
			continue
		}
		file.Data, file.Size = data, int64(len(data))
		out = append(out, file)
	}
	return out
}

func walkMemberFiles(dir string, visit func(name string, info fs.FileInfo)) {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && path != dir && entry.Name() == memberTmpDirName && filepath.Dir(path) == dir {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		visit(filepath.ToSlash(rel), info)
		return nil
	})
}
