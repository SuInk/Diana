// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/SuInk/diana/internal/procgroup"
)

// memberBaseReadRoots 是成员沙盒在任何平台都放开读取的系统位置：动态库、解释器、
// 证书、时区。家目录、/opt 下的其他软件数据、工作区都不在里面。
func memberBaseReadRoots() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"/usr", "/bin", "/sbin", "/System", "/Library/Frameworks", "/Library/Fonts",
			// 只放必要的证书、时区和基础解析表，不整片放开 /private/etc（避免暴露 /private/etc/ssh 等）与 /dev/disk*。
			"/private/etc/ssl", "/private/etc/localtime", "/private/etc/hosts", "/private/etc/resolv.conf",
			"/private/etc/services", "/private/etc/protocols",
			"/private/var/db/timezone", "/private/var/select",
			"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/dtracehelper", "/dev/fd",
			// Homebrew 只放程序本体和库，不放 var（数据库、服务数据）和 etc 里的其他配置。
			"/opt/homebrew/Cellar", "/opt/homebrew/opt", "/opt/homebrew/lib", "/opt/homebrew/share",
			"/opt/homebrew/Frameworks", "/opt/homebrew/etc/openssl@3", "/opt/homebrew/etc/ca-certificates",
			"/opt/homebrew/etc/fonts",
		}
	}
	return []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/etc/ld.so.cache", "/etc/ld.so.conf",
		"/etc/ld.so.conf.d", "/etc/alternatives", "/etc/ssl", "/etc/ca-certificates", "/etc/localtime", "/etc/fonts"}
}

// memberDarwinDeniedReads 是白名单子路径里仍要挡掉的数据目录：Intel 版 Homebrew
// 装在 /usr/local 下，它的 var、etc 和 /opt/homebrew/var 一样是服务数据和配置。
var memberDarwinDeniedReads = []string{"/usr/local/var", "/usr/local/etc"}

// memberSandboxExecProfile 生成成员用的 SBPL 策略。和主人的 sandboxExecProfile 是
// 两份：主人那份先 (allow file-read*) 再逐条挡凭据，这份从零开始逐条放。
//
//   - file-read-metadata 全放：解析任何路径都要 stat 沿途每一级，不放连 /bin/sh 都起
//     不来。它只泄露「某个路径存在、多大」，读不到内容，也列不出目录。
//   - 不放 process-fork：sandbox-exec 和 sh 都是 exec 下一个程序，不需要 fork；禁掉
//     之后 fork 炸弹和逃出进程组的孤儿进程都不存在，进程数恒为 1。
//   - 显式拒绝高危 Mach IPC（LaunchServices/AppleEvents/剪贴板/钥匙串/后台 URL 会话）和硬链接，
//     堵住借宿主服务拉起沙盒外进程、偷剪贴板、绕过断网和硬链接系统文件。
//   - 不放网络，也不放 /tmp 写入，临时文件走成员目录下的 .tmp。
func memberSandboxExecProfile(spec memberSandboxSpec) string {
	var builder strings.Builder
	builder.WriteString("(version 1)(deny default)(deny file-link)")
	builder.WriteString("(allow process-exec)(allow signal (target self))(allow sysctl-read)(allow mach-lookup)")
	writeSBPLDeniedMachServices(&builder, sandboxDeniedEscapeMachServices...)
	writeSBPLDeniedMachServices(&builder, sandboxDeniedNetProxyMachServices...)
	builder.WriteString("(allow file-read-metadata)")
	builder.WriteString(`(allow file-read* (literal "/")`)
	for _, path := range append(memberBaseReadRoots(), spec.readRoots...) {
		builder.WriteString(" (subpath " + sbplString(path) + ")")
	}
	for _, path := range sandboxWritableRoots(spec.dir) {
		builder.WriteString(" (subpath " + sbplString(path) + ")")
	}
	builder.WriteString(")")
	for _, path := range memberDarwinDeniedReads {
		builder.WriteString("(deny file-read* (subpath " + sbplString(path) + "))")
	}
	// Intel 版 Homebrew 的 OpenSSL 配置和证书在被挡的 /usr/local/etc 里，node 启动就读，
	// 挡掉就起不来。SBPL 后写覆盖先写，这几行要跟在 deny 后面。
	builder.WriteString(`(allow file-read* (subpath "/usr/local/etc/openssl@3") (subpath "/usr/local/etc/ca-certificates"))`)
	builder.WriteString("(allow file-write*")
	for _, path := range sandboxWritableRoots(spec.dir) {
		builder.WriteString(" (subpath " + sbplString(path) + ")")
	}
	builder.WriteString(")")
	builder.WriteString(`(allow file-write-data (literal "/dev/null"))`)
	return builder.String()
}

func memberSandboxExecCommand(sandboxExecPath string) func(context.Context, memberSandboxSpec) *exec.Cmd {
	return func(ctx context.Context, spec memberSandboxSpec) *exec.Cmd {
		spec.readRoots = memberReadRootsOutside(spec.readRoots, spec.dir)
		args := append([]string{"-p", memberSandboxExecProfile(spec)}, memberShellArgs(spec)...)
		return procgroup.CommandContext(ctx, sandboxExecPath, args...)
	}
}

// memberBubblewrapWorkDir 是 bubblewrap 里成员目录的挂载点。
const memberBubblewrapWorkDir = "/work"

// memberBubblewrapArgs 生成成员用的 bwrap 参数。主人那份是 --ro-bind / /，这份从空的
// 根开始，只挂白名单里存在的路径：
//
//   - /bin、/lib 这类在新发行版上是指向 usr 的符号链接，原样建成链接，而不是再挂一份；
//   - 成员目录挂到 /work 可写，其他一律只读；
//   - --unshare-all 摘掉网络、用户、PID、IPC、UTS 命名空间；PID 命名空间里的 1 号进程
//     一退出，里面剩下的进程全部被内核杀掉，没有孤儿能留下来。
func memberBubblewrapArgs(spec memberSandboxSpec) []string {
	args := []string{"--unshare-all", "--die-with-parent", "--new-session"}
	for _, path := range append(memberBaseReadRoots(), spec.readRoots...) {
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(path); err == nil {
				args = append(args, "--symlink", target, path)
				continue
			}
		}
		args = append(args, "--ro-bind", path, path)
	}
	args = append(args,
		"--dev", "/dev",
		"--proc", "/proc",
		"--bind", spec.dir, memberBubblewrapWorkDir,
		// /tmp 指到成员目录里的 .tmp：tmpfs 吃的是宿主机内存，又不算进进程 RSS，
		// 看门狗管不住；落在成员目录里就归磁盘配额管。
		"--symlink", memberBubblewrapWorkDir+"/"+memberTmpDirName, "/tmp",
		"--chdir", memberBubblewrapWorkDir,
		// 根和 /dev 都是 bwrap 建的 tmpfs，留着可写就是一块不限量的内存盘（/dev/shm
		// 也在里面）。最后把它们改成只读；设备节点照常能写，/work 是单独的挂载不受影响。
		"--remount-ro", "/dev",
		"--remount-ro", "/",
		"--",
	)
	return append(args, memberShellArgs(spec)...)
}

func memberBubblewrapCommand(bwrapPath string) func(context.Context, memberSandboxSpec) *exec.Cmd {
	return func(ctx context.Context, spec memberSandboxSpec) *exec.Cmd {
		spec.readRoots = memberReadRootsOutside(spec.readRoots, spec.dir)
		return procgroup.CommandContext(ctx, bwrapPath, memberBubblewrapArgs(spec)...)
	}
}

// memberReadRootsOutside 去掉包住成员目录的额外读取路径：成员目录的上一级就是别的
// 成员、别的群。
func memberReadRootsOutside(roots []string, dir string) []string {
	out := roots[:0:0]
	for _, root := range roots {
		if !pathWithin(root, dir) {
			out = append(out, root)
		}
	}
	return out
}

// processTreeStats 统计 pid 及其全部子孙进程的个数和常驻内存（字节）。
func processTreeStats(pid int) (int, int64, error) {
	var table map[int][2]int64
	var err error
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		table, err = procProcessTable()
	} else {
		table, err = psProcessTable()
	}
	if err != nil {
		return 0, 0, err
	}
	children := map[int][]int{}
	for child, row := range table {
		children[int(row[0])] = append(children[int(row[0])], child)
	}
	count, rss := 0, int64(0)
	queue := []int{pid}
	seen := map[int]bool{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		row, ok := table[current]
		if !ok {
			continue
		}
		count++
		rss += row[1]
		queue = append(queue, children[current]...)
	}
	return count, rss, nil
}

// psProcessTable 用 ps 取 pid → {ppid, rss 字节}，macOS 没有 /proc。
func psProcessTable() (map[int][2]int64, error) {
	output, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,rss=").Output()
	if err != nil {
		return nil, err
	}
	table := map[int][2]int64{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.ParseInt(fields[1], 10, 64)
		rss, err3 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		table[pid] = [2]int64{ppid, rss << 10}
	}
	return table, nil
}

// procProcessTable 从 /proc/<pid>/stat 取 pid → {ppid, rss 字节}。进程名可能带空格
// 和括号，字段从最后一个 ')' 之后数。
func procProcessTable() (map[int][2]int64, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	page := int64(os.Getpagesize())
	table := map[int][2]int64{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		stat := string(data)
		end := strings.LastIndexByte(stat, ')')
		if end < 0 {
			continue
		}
		// ) 之后依次是 state ppid ... rss 在第 22 个（从 state 记为 0）。
		fields := strings.Fields(stat[end+1:])
		if len(fields) < 22 {
			continue
		}
		ppid, err1 := strconv.ParseInt(fields[1], 10, 64)
		pages, err2 := strconv.ParseInt(fields[21], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		table[pid] = [2]int64{ppid, pages * page}
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("no processes in /proc")
	}
	return table, nil
}
