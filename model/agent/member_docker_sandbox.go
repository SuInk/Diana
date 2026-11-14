// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
)

const (
	// DefaultMemberSandboxImage 是成员离线容器沙盒的默认镜像名（见 deploy/sandbox/Dockerfile）。
	DefaultMemberSandboxImage = "diana-sandbox:latest"

	memberSandboxBackendEnv    = "DIANA_MEMBER_SANDBOX_BACKEND"
	memberSandboxImageEnv      = "DIANA_MEMBER_SANDBOX_IMAGE"
	memberSandboxDockerHostEnv = "DIANA_MEMBER_SANDBOX_DOCKER_HOST"

	memberDockerWorkDir      = "/work"
	memberDockerTmpDir       = "/tmp"
	memberDockerMinPIDsLimit = 64
	memberDockerInspectWait  = 3 * time.Second
	memberDockerKillWait     = 4 * time.Second
	memberDockerHealthyTTL   = 30 * time.Second
	memberDockerRetryTTL     = 15 * time.Second
)

type memberDockerConfig struct {
	dockerBin  string
	dockerHost string
	image      string
	userSpec   string
}

var memberDockerCache struct {
	mu        sync.Mutex
	sandbox   memberSandbox
	reason    string
	expiresAt time.Time
}

func invalidateMemberDockerCache() {
	memberDockerCache.mu.Lock()
	memberDockerCache.expiresAt = time.Time{}
	memberDockerCache.sandbox = memberSandbox{}
	memberDockerCache.mu.Unlock()
}

func cachedAutoMemberDockerSandbox() (memberSandbox, bool) {
	now := time.Now()
	memberDockerCache.mu.Lock()
	if now.Before(memberDockerCache.expiresAt) {
		sb := memberDockerCache.sandbox
		memberDockerCache.mu.Unlock()
		return sb, sb.available()
	}
	memberDockerCache.mu.Unlock()

	cfg, reason := resolveMemberDockerConfig()
	var candidate memberSandbox
	ttl := memberDockerRetryTTL
	if reason == "" {
		candidate = newMemberDockerSandbox(cfg)
		if err := memberSandboxProbe(candidate); err != nil {
			reason = err.Error()
			candidate = memberSandbox{}
		} else {
			ttl = memberDockerHealthyTTL
		}
	}

	memberDockerCache.mu.Lock()
	memberDockerCache.sandbox = candidate
	memberDockerCache.reason = reason
	memberDockerCache.expiresAt = time.Now().Add(ttl)
	memberDockerCache.mu.Unlock()
	return candidate, candidate.available()
}

// isRunningInsideContainer 判断 Diana 自身是否运行在容器内。
// 若 Diana 自己就在容器里，宿主机 dockerd 看到的路径命名空间与容器内部不一致，
// auto 模式不应自动启用兄弟容器 bind mount（应走容器内的原生 bubblewrap）。
func isRunningInsideContainer() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DIANA_DEPLOYMENT")), "docker") {
		return true
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func resolveMemberDockerConfig() (memberDockerConfig, string) {
	image := strings.TrimSpace(os.Getenv(memberSandboxImageEnv))
	if image == "" {
		image = DefaultMemberSandboxImage
	}
	dockerHost := strings.TrimSpace(os.Getenv(memberSandboxDockerHostEnv))
	dockerBin, err := lookupDockerBinary()
	if err != nil {
		return memberDockerConfig{}, "这台机器上找不到 docker CLI"
	}
	ctx, cancel := context.WithTimeout(context.Background(), memberDockerInspectWait)
	defer cancel()

	clientEnv := minimalDockerClientEnv()
	inspectArgs := dockerHostPrefix(dockerHost, "image", "inspect", "--format", "{{.Id}}", image)
	cmd := exec.CommandContext(ctx, dockerBin, inspectArgs...)
	cmd.Env = clientEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return memberDockerConfig{}, fmt.Sprintf("未就绪离线沙盒镜像 %s（可执行 docker build -t %s deploy/sandbox 构建）：%s", image, DefaultMemberSandboxImage, detail)
	}

	rootless := false
	infoArgs := dockerHostPrefix(dockerHost, "info", "--format", "{{json .SecurityOptions}}")
	infoCmd := exec.CommandContext(ctx, dockerBin, infoArgs...)
	infoCmd.Env = clientEnv
	if infoOut, err := infoCmd.Output(); err == nil && strings.Contains(strings.ToLower(string(infoOut)), "rootless") {
		rootless = true
	}

	return memberDockerConfig{
		dockerBin:  dockerBin,
		dockerHost: dockerHost,
		image:      image,
		userSpec:   memberDockerUserSpecFor(rootless),
	}, ""
}

func lookupDockerBinary() (string, error) {
	if p, err := exec.LookPath("docker"); err == nil {
		return p, nil
	}
	candidates := []string{"/usr/local/bin/docker", "/usr/bin/docker"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{
			"/opt/homebrew/bin/docker",
			"/Applications/Docker.app/Contents/Resources/bin/docker",
		}, candidates...)
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", &exec.Error{Name: "docker", Err: exec.ErrNotFound}
}

func dockerHostPrefix(dockerHost string, args ...string) []string {
	if strings.TrimSpace(dockerHost) == "" {
		return args
	}
	return append([]string{"--host", strings.TrimSpace(dockerHost)}, args...)
}

func minimalDockerClientEnv() []string {
	pathVal := "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		if sysPath := os.Getenv("PATH"); sysPath != "" {
			pathVal = sysPath
		}
	}
	env := []string{
		"PATH=" + pathVal,
		"LC_ALL=C",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env = append(env, "HOME="+home)
		if runtime.GOOS == "windows" {
			env = append(env, "USERPROFILE="+home)
		}
	}
	for _, key := range []string{"DOCKER_CONFIG", "SYSTEMROOT", "TEMP", "TMP"} {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}

func memberDockerUserSpecFor(rootless bool) string {
	if rootless {
		// Rootless Docker 的用户命名空间将非特权宿主用户映射为容器内 0:0；
		// 若指定 1000:1000 会被映射到宿主 subuid，反而无权读写宿主 0700 的个人目录。
		return "0:0"
	}
	if runtime.GOOS == "linux" {
		uid, gid := os.Geteuid(), os.Getegid()
		if uid > 0 && gid > 0 {
			return fmt.Sprintf("%d:%d", uid, gid)
		}
	}
	return "1000:1000"
}

func randomMemberContainerName() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("diana-sbx-%d", time.Now().UnixNano())
	}
	return "diana-sbx-" + hex.EncodeToString(buf[:])
}

// memberDockerRunArgs 组装离线加固容器参数：
//   - --rm --init：单次用完即焚、自动回收子进程，绝不跨会话或跨群友共享常驻容器；
//   - --network none：100% 物理断网，无虚拟网卡与宿主机网关，封死外联与内网探测；
//   - --cap-drop ALL + no-new-privileges + --read-only + --ipc none：堵死特权提权与逃逸面；
//   - 不挂载可写内存盘（/dev/shm 只读），将宿主机 <dir>/.tmp 挂载到容器 /tmp，既兼容硬编码 /tmp 的二进制又受磁盘配额看门狗实时监管；
//   - cgroup 硬限内存（禁 Swap）、CPU 与 PID 数，同时允许容器内 Chromium / Matplotlib / FFmpeg 正常多进程协作；
//   - 仅挂载当前成员在本群的独立目录到 /work，不传入任何宿主机环境变量或凭据。
func memberDockerRunArgs(cfg memberDockerConfig, containerName string, spec memberSandboxSpec) []string {
	memBytes := spec.limits.MemoryBytes
	if memBytes <= 0 {
		memBytes = DefaultMemberCommandLimits.MemoryBytes
	}
	pidsLimit := spec.limits.Processes
	if pidsLimit < memberDockerMinPIDsLimit {
		pidsLimit = memberDockerMinPIDsLimit
	}
	userSpec := cfg.userSpec
	if userSpec == "" {
		userSpec = memberDockerUserSpecFor(false)
	}
	memFlag := strconv.FormatInt(memBytes, 10)
	cleanDir := filepath.Clean(spec.dir)
	hostTmpDir := filepath.Join(cleanDir, memberTmpDirName)
	pyLibPath := memberDockerTmpDir + "/" + memberPythonLibSubdir

	runArgs := []string{
		"run",
		"--rm",
		"--init",
		"--interactive",
		"--name", containerName,
		"--hostname", "sandbox",
		"--user", userSpec,
		"--network", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--ipc", "none",
		"--tmpfs", "/dev/shm:ro,nosuid,nodev,noexec,size=64k",
		"--memory", memFlag,
		"--memory-swap", memFlag,
		"--cpus", "1.0",
		"--pids-limit", strconv.Itoa(pidsLimit),
		"--env", "PATH=/usr/local/bin:/usr/bin:/bin",
		"--env", "HOME=" + memberDockerTmpDir,
		"--env", "TMPDIR=" + memberDockerTmpDir,
		"--env", "TMP=" + memberDockerTmpDir,
		"--env", "TEMP=" + memberDockerTmpDir,
		"--env", "PYTHONPATH=" + pyLibPath,
		"--env", "MPLCONFIGDIR=" + memberDockerTmpDir + "/mpl",
		"--env", "XDG_CACHE_HOME=" + memberDockerTmpDir + "/cache",
		"--env", "DBUS_SESSION_BUS_ADDRESS=/dev/null",
		"--env", "PYTHONDONTWRITEBYTECODE=1",
		"--env", "PYTHONNOUSERSITE=1",
		"--env", "NO_COLOR=1",
		"--env", "LANG=C.UTF-8",
		"--env", "LC_ALL=C.UTF-8",
		"--env", "TZ=UTC",
		"--volume", cleanDir + ":" + memberDockerWorkDir + ":rw",
		"--volume", hostTmpDir + ":" + memberDockerTmpDir + ":rw",
		"--workdir", memberDockerWorkDir,
		cfg.image,
	}
	runArgs = append(runArgs, memberShellArgs(spec)...)
	return dockerHostPrefix(cfg.dockerHost, runArgs...)
}

func isDockerInfraFailure(exitCode int, output string) bool {
	if exitCode != 125 && exitCode != -1 {
		return false
	}
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"is the docker daemon running",
		"error during connect",
		"mounts denied",
		"mount denied",
		"error while creating mount source path",
		"no such image",
		"docker: error response from daemon",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// memberContainerUnsupportedPrograms 列出仅在特定宿主系统存在、Linux 容器镜像不提供的命令名。
var memberContainerUnsupportedPrograms = map[string]bool{
	"sips": true, // macOS 专属，容器内请用 magick / convert / ffmpeg / python3
	"md5":  true, // BSD/macOS 专属，容器内有标准 md5sum / sha256sum / shasum
}

func availableMemberContainerPrograms() []string {
	out := make([]string, 0, len(memberCommandPrograms))
	for _, name := range memberCommandPrograms {
		if !memberContainerUnsupportedPrograms[name] {
			out = append(out, name)
		}
	}
	return out
}

func removeMemberDockerContainer(cfg memberDockerConfig, containerName string) {
	if containerName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), memberDockerKillWait)
	defer cancel()
	args := dockerHostPrefix(cfg.dockerHost, "rm", "--force", containerName)
	cmd := exec.CommandContext(ctx, cfg.dockerBin, args...)
	cmd.Env = minimalDockerClientEnv()
	_ = cmd.Run()
}

func newMemberDockerSandbox(cfg memberDockerConfig) memberSandbox {
	return memberSandbox{
		kind:        "docker",
		inContainer: true,
		view:        func(string) string { return memberDockerWorkDir },
		build: func(ctx context.Context, spec memberSandboxSpec) *exec.Cmd {
			containerName := randomMemberContainerName()
			args := memberDockerRunArgs(cfg, containerName, spec)
			cmd := procgroup.CommandContext(ctx, cfg.dockerBin, args...)
			cmd.Env = minimalDockerClientEnv()
			origCancel := cmd.Cancel
			cmd.Cancel = func() error {
				// docker run 客户端被杀时，守护进程里的容器不会自动退出；显式 rm -f 确保零残留。
				removeMemberDockerContainer(cfg, containerName)
				if origCancel != nil {
					return origCancel()
				}
				if cmd.Process != nil {
					return cmd.Process.Kill()
				}
				return nil
			}
			return cmd
		},
	}
}
