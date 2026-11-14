// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/deploy/box"
)

// 工作区容器（work box），照雨夹雪 src/sandbox/box.ts：一个常驻容器加一个命名卷
// /work，所有命令都在里面跑。隔离交给 Docker：不挂任何宿主目录、不传环境变量、
// 根文件系统只读、cap-drop ALL、no-new-privileges、限内存/CPU/进程数。容器里没有
// 任何机密，最坏也只是把这个卷弄乱，box_reset 一下就回到干净状态。
//
// 和雨夹雪的一处差别：联网时 box 接在专用 bridge 网络上，并由一次性的防火墙容器在
// box 的网络命名空间里丢掉发往宿主机、私网和 Tailscale 的包。雨夹雪跑在 Linux
// rootless Docker 上，slirp4netns 默认就够不到宿主回环；Docker Desktop 的容器却能经
// host.docker.internal 直达宿主机 127.0.0.1 上的各种服务，所以这一层必须补上。

const (
	BoxWorkDir = "/work"
	// boxInboxDir 是聊天附件落进 box 的目录（= 雨夹雪 tg_download 的 /work/inbox）。
	boxInboxDir = BoxWorkDir + "/inbox"
	// boxOutputDir 存命令的完整输出，结果放不下时模型按行读它。
	boxOutputDir = BoxWorkDir + "/.output"

	boxNetworkName = "diana-box"
	boxLabel       = "diana.box"
	// boxKeyLabel 存 key 的十六进制：key 里有 \x00 分隔符，不能直接进命令行参数。
	boxKeyLabel = "diana.box.key"

	// boxControlTimeout 是 inspect/run/rm 这类控制命令的超时（= 雨夹雪的 60 秒）。
	boxControlTimeout = 60 * time.Second
	// boxBuildTimeout 是首次构建镜像的超时：要装 chromium、ffmpeg 和 CJK 字体。
	boxBuildTimeout = 30 * time.Minute

	// BoxCommandDefaultTimeout / BoxCommandMaxTimeout 照雨夹雪包 Pi bash 的那一层：默认 60 秒，最多 600 秒。
	BoxCommandDefaultTimeout = 60 * time.Second
	BoxCommandMaxTimeout     = 600 * time.Second

	// BoxCopyOutMaxBytes 是 send_file 从 box 取文件的上限。
	BoxCopyOutMaxBytes = 100 << 20
)

const (
	boxImageEnv      = "DIANA_BOX_IMAGE"
	boxDockerHostEnv = "DIANA_BOX_DOCKER_HOST"
	boxMemoryEnv     = "DIANA_BOX_MEMORY"
	boxCPUsEnv       = "DIANA_BOX_CPUS"
	boxPIDsEnv       = "DIANA_BOX_PIDS"
	boxNetworkEnv    = "DIANA_BOX_NETWORK"
)

// BoxConfig 是 work box 的部署设置（= 雨夹雪 config.ts 的 sandbox 段）。
type BoxConfig struct {
	// Image 留空时用内置 Dockerfile 本地构建的 diana-box:<哈希>；自定义镜像由部署方自己准备。
	Image      string
	DockerHost string
	Memory     string
	CPUs       float64
	PIDs       int
	Network    bool
}

// BuiltinBoxImage 是内置镜像的标签，带 Dockerfile 内容哈希：改了镜像定义，旧容器按
// 「镜像不一致」重建（雨夹雪靠手动改 tg-box:N）。
func BuiltinBoxImage() string {
	sum := sha256.Sum256([]byte(box.Dockerfile))
	return "diana-box:" + hex.EncodeToString(sum[:])[:12]
}

// BoxConfigFromEnv 读部署方的覆盖项，其余照雨夹雪的默认值。
func BoxConfigFromEnv() BoxConfig {
	cfg := BoxConfig{
		Image:      strings.TrimSpace(os.Getenv(boxImageEnv)),
		DockerHost: strings.TrimSpace(os.Getenv(boxDockerHostEnv)),
		Memory:     strings.TrimSpace(os.Getenv(boxMemoryEnv)),
		Network:    true,
	}
	if cfg.Image == "" {
		cfg.Image = BuiltinBoxImage()
	}
	if cfg.Memory == "" {
		cfg.Memory = "1g"
	}
	cfg.CPUs = 2
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(boxCPUsEnv)), 64); err == nil && v >= 0.1 && v <= 64 {
		cfg.CPUs = v
	}
	cfg.PIDs = 256
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(boxPIDsEnv))); err == nil && v >= 16 && v <= 65536 {
		cfg.PIDs = v
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(boxNetworkEnv))) {
	case "0", "false", "off", "no":
		cfg.Network = false
	}
	return cfg
}

// BoxError 是给模型和用户看的 box 故障。
type BoxError struct{ msg string }

func (e *BoxError) Error() string { return e.msg }

func boxErrorf(format string, args ...any) error { return &BoxError{msg: fmt.Sprintf(format, args...)} }

// dockerCall 是一次 docker CLI 调用。stdout 为 nil 时输出收进 result.stdout。
type dockerCall struct {
	args    []string
	stdin   io.Reader
	stdout  io.Writer
	timeout time.Duration
}

type dockerResult struct {
	code     int
	stdout   []byte
	stderr   string
	timedOut bool
}

// dockerRunner 执行 docker CLI；测试里替换它。
type dockerRunner func(ctx context.Context, call dockerCall) dockerResult

func execDocker(dockerBin, dockerHost string) dockerRunner {
	return func(ctx context.Context, call dockerCall) dockerResult {
		if call.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, call.timeout)
			defer cancel()
		}
		cmd := exec.CommandContext(ctx, dockerBin, dockerHostPrefix(dockerHost, call.args...)...)
		// 雨夹雪只给 docker 客户端 PATH；Diana 另留 HOME/DOCKER_CONFIG 让 CLI 找到上下文。
		cmd.Env = minimalDockerClientEnv()
		cmd.Stdin = call.stdin
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		if call.stdout != nil {
			cmd.Stdout = call.stdout
		}
		cmd.Stderr = &errOut
		err := cmd.Run()
		res := dockerResult{stdout: out.Bytes(), stderr: errOut.String(), timedOut: ctx.Err() != nil}
		if err != nil {
			res.code = -1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
				res.code = exitErr.ExitCode()
			}
			if res.stderr == "" {
				res.stderr = err.Error()
			}
		}
		return res
	}
}

// boxIsolationArgs 是雨夹雪 isolationArgs 的逐项对应，外加专用网络。
func boxIsolationArgs(network bool, volume string) []string {
	netMode := "none"
	if network {
		netMode = boxNetworkName
	}
	return []string{
		"--user", "1000:1000",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--network", netMode,
		"--env", "HOME=" + BoxWorkDir + "/.home",
		"--volume", volume + ":" + BoxWorkDir,
	}
}

// boxFirewallScript 在 box 的网络命名空间里只放行公网：回环（Docker 内置 DNS
// 127.0.0.11 走它）和已建立的连接放行，私网、宿主机（host.docker.internal 落在
// 192.168.65.0/24 或 fdc4::/16）、Tailscale 100.64.0.0/10、链路本地、组播一律拒绝。
// box 自己没有 NET_ADMIN，改不了这些规则。
const boxFirewallScript = `set -e
PATH=/usr/sbin:/sbin:$PATH
for t in iptables ip6tables; do
  $t -w -P OUTPUT ACCEPT
  $t -w -F OUTPUT
  $t -w -A OUTPUT -o lo -j ACCEPT
  $t -w -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
done
for n in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do
  iptables -w -A OUTPUT -d "$n" -j REJECT
done
for n in ::/128 ::1/128 ::ffff:0:0/96 64:ff9b::/96 fc00::/7 fe80::/10 ff00::/8; do
  ip6tables -w -A OUTPUT -d "$n" -j REJECT
done
`

// WorkBox 是一个 work box：容器名和卷名由 key 决定。
type WorkBox struct {
	cfg       BoxConfig
	docker    dockerRunner
	key       string
	container string
	volume    string

	// lifecycle 串行化 ensure / reset / interrupt（= 雨夹雪的 exclusive）。
	lifecycle sync.Mutex
	// firewalled 表示本进程已经给当前这个容器写过防火墙：Diana 重启后接手旧容器时
	// 不知道上一个进程有没有写完，所以每个进程第一次用都重写一遍（规则是幂等的）。
	firewalled bool
	running    atomic.Int32
	// lastUsed 是最近一次用 box 的时间（UnixNano），空闲回收按它判断。
	lastUsed atomic.Int64
}

// boxNameFor 把任意 key 变成容器名安全的后缀。
func boxNameFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

func newWorkBox(cfg BoxConfig, docker dockerRunner, key string) *WorkBox {
	suffix := boxNameFor(key)
	return &WorkBox{cfg: cfg, docker: docker, key: key, container: "diana-box-" + suffix, volume: "diana-work-" + suffix}
}

func (b *WorkBox) call(ctx context.Context, timeout time.Duration, args ...string) dockerResult {
	return b.docker(ctx, dockerCall{args: args, timeout: timeout})
}

func (b *WorkBox) runArgs() []string {
	args := []string{
		"run", "--detach",
		"--name", b.container,
		"--hostname", "box",
		// PID 1 若是 sleep，超时被杀的后台进程会变成收不掉的僵尸，慢慢占满 pids 上限；
		// --init 让 tini 回收它们。
		"--init",
		"--label", boxLabel + "=1",
		"--label", boxKeyLabel + "=" + hex.EncodeToString([]byte(b.key)),
	}
	args = append(args, boxIsolationArgs(b.cfg.Network, b.volume)...)
	return append(args,
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--memory", b.cfg.Memory,
		"--cpus", strconv.FormatFloat(b.cfg.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.Itoa(b.cfg.PIDs),
		"--workdir", BoxWorkDir,
		b.cfg.Image,
		"sleep", "infinity",
	)
}

func (b *WorkBox) firewallArgs() []string {
	return []string{
		"run", "--rm",
		"--network", "container:" + b.container,
		"--cap-drop", "ALL",
		"--cap-add", "NET_ADMIN",
		"--cap-add", "NET_RAW",
		"--security-opt", "no-new-privileges",
		"--user", "0:0",
		"--entrypoint", "/bin/sh",
		b.cfg.Image,
		"-c", boxFirewallScript,
	}
}

// Ensure 确保容器在跑、镜像对、防火墙已写（= 雨夹雪 ensure/start）。
func (b *WorkBox) Ensure(ctx context.Context) error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	return b.start(ctx)
}

func (b *WorkBox) start(ctx context.Context) error {
	state := b.call(ctx, boxControlTimeout, "inspect", "--format", "{{.State.Running}} {{.Config.Image}}", b.container)
	if state.code == 0 && strings.TrimSpace(string(state.stdout)) == "true "+b.cfg.Image {
		if b.firewalled || !b.cfg.Network {
			return nil
		}
		if err := b.applyFirewall(ctx); err != nil {
			_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
			return err
		}
		return nil
	}
	b.firewalled = false
	if state.code == 0 {
		_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
	}
	if err := b.ensureImage(ctx); err != nil {
		return err
	}
	if b.cfg.Network {
		if err := b.ensureNetwork(ctx); err != nil {
			return err
		}
	}
	// 先带标签建卷：每周重置按卷的创建时间判断这个工作区上次清空是什么时候。
	if res := b.call(ctx, boxControlTimeout, "volume", "create", "--label", boxLabel+"=1", "--label", boxKeyLabel+"="+hex.EncodeToString([]byte(b.key)), b.volume); res.code != 0 {
		return boxErrorf("工作区卷创建失败：%s", strings.TrimSpace(res.stderr))
	}
	if res := b.call(ctx, boxControlTimeout, b.runArgs()...); res.code != 0 {
		return boxErrorf("工作区容器启动失败：%s", strings.TrimSpace(res.stderr))
	}
	if res := b.call(ctx, boxControlTimeout, "exec", b.container, "mkdir", "-p", BoxWorkDir+"/.home", boxInboxDir, boxOutputDir); res.code != 0 {
		return boxErrorf("工作区初始化失败：%s", strings.TrimSpace(res.stderr))
	}
	if b.cfg.Network {
		if err := b.applyFirewall(ctx); err != nil {
			// 没写上防火墙的联网容器不能留着。
			_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
			return err
		}
	}
	return nil
}

func (b *WorkBox) applyFirewall(ctx context.Context) error {
	if res := b.call(ctx, boxControlTimeout, b.firewallArgs()...); res.code != 0 {
		return boxErrorf("工作区出站防火墙写入失败：%s", strings.TrimSpace(res.stderr))
	}
	b.firewalled = true
	return nil
}

// boxBuildMu 防止多个 box 同时构建同一个镜像。
var boxBuildMu sync.Mutex

func (b *WorkBox) ensureImage(ctx context.Context) error {
	return ensureBoxImage(ctx, b.docker, b.cfg.Image)
}

func ensureBoxImage(ctx context.Context, docker dockerRunner, image string) error {
	inspect := func() bool {
		return docker(ctx, dockerCall{args: []string{"image", "inspect", "--format", "{{.Id}}", image}, timeout: boxControlTimeout}).code == 0
	}
	if inspect() {
		return nil
	}
	if image != BuiltinBoxImage() {
		return boxErrorf("找不到工作区镜像 %s，自定义镜像需要部署方先准备好", image)
	}
	boxBuildMu.Lock()
	defer boxBuildMu.Unlock()
	if inspect() {
		return nil
	}
	res := docker(ctx, dockerCall{args: []string{"build", "--tag", image, "-"}, stdin: strings.NewReader(box.Dockerfile), timeout: boxBuildTimeout})
	if res.code != 0 {
		return boxErrorf("工作区镜像构建失败：%s", lastLines(res.stderr, 20))
	}
	return nil
}

// PrewarmBoxImage 在启动时把内置镜像构建好，免得第一条命令等好几分钟。
func PrewarmBoxImage(ctx context.Context) error {
	docker, reason := boxRuntime()
	if reason != "" {
		return boxErrorf("%s", reason)
	}
	return ensureBoxImage(ctx, docker, BoxConfigFromEnv().Image)
}

func (b *WorkBox) ensureNetwork(ctx context.Context) error {
	if b.call(ctx, boxControlTimeout, "network", "inspect", "--format", "{{.Id}}", boxNetworkName).code == 0 {
		return nil
	}
	res := b.call(ctx, boxControlTimeout, "network", "create", "--driver", "bridge", "--label", boxLabel+"=1", boxNetworkName)
	if res.code != 0 && !strings.Contains(res.stderr, "already exists") {
		return boxErrorf("工作区网络创建失败：%s", strings.TrimSpace(res.stderr))
	}
	return nil
}

// Interrupt 杀掉容器，下次使用时重建（= 雨夹雪 interrupt）。
func (b *WorkBox) Interrupt(ctx context.Context) {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	_ = b.call(ctx, boxControlTimeout, "kill", b.container)
	b.firewalled = false
}

// Reset 删容器和卷（= 雨夹雪 reset/remove）。
func (b *WorkBox) Reset(ctx context.Context) error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.firewalled = false
	if res := b.call(ctx, boxControlTimeout, "rm", "--force", b.container); res.code != 0 {
		if b.call(ctx, boxControlTimeout, "inspect", b.container).code == 0 {
			return boxErrorf("工作区容器删除失败：%s", strings.TrimSpace(res.stderr))
		}
	}
	if res := b.call(ctx, boxControlTimeout, "volume", "rm", "--force", b.volume); res.code != 0 {
		return boxErrorf("工作区清空失败：%s", strings.TrimSpace(res.stderr))
	}
	return nil
}

// Busy 报告有没有操作正在进行；每周重置和空闲回收都跳过忙着的 box。
func (b *WorkBox) Busy() bool { return b.running.Load() > 0 }

// use 标记一次使用的开始；返回的函数标记结束。两头都刷新 lastUsed。
func (b *WorkBox) use() func() {
	b.running.Add(1)
	b.lastUsed.Store(time.Now().UnixNano())
	return func() {
		b.lastUsed.Store(time.Now().UnixNano())
		b.running.Add(-1)
	}
}

// Stop 删掉容器、保留卷：空闲回收用，下次使用时重建，/work 里的东西还在。
func (b *WorkBox) Stop(ctx context.Context) {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
	b.firewalled = false
}

// BoxExecResult 是一次命令的结果；输出合并 stdout/stderr 写在调用方给的 writer 里。
type BoxExecResult struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
}

// boxExecScript 让 timeout 成为进程组长（GNU timeout 会 setpgid），记下它的 pid：超时
// 由 timeout 自己杀整组；调用方取消时 Diana 照 pid 杀组。docker exec 客户端被杀并不会
// 结束容器里的进程，这一步对应雨夹雪里 pi-env 的取消。
const boxExecScript = `echo $$ > "/tmp/.run-$1" && exec timeout --kill-after=5 "$2" bash -c "$3" 2>&1`

// Exec 在 box 里用 bash 跑 command。
func (b *WorkBox) Exec(ctx context.Context, command string, timeout time.Duration, output io.Writer) (BoxExecResult, error) {
	if timeout <= 0 {
		timeout = BoxCommandDefaultTimeout
	}
	timeout = min(timeout, BoxCommandMaxTimeout)
	defer b.use()()
	for attempt := 0; ; attempt++ {
		if err := b.Ensure(ctx); err != nil {
			return BoxExecResult{}, err
		}
		runID := randomBoxRunID()
		start := time.Now()
		res := b.docker(ctx, dockerCall{
			args:    []string{"exec", "--workdir", BoxWorkDir, b.container, "/bin/sh", "-c", boxExecScript, "sh", runID, strconv.Itoa(int(timeout / time.Second)), command},
			stdout:  output,
			timeout: timeout + 15*time.Second,
		})
		duration := time.Since(start)
		if ctx.Err() != nil || res.timedOut {
			b.killRun(runID)
		} else {
			_ = b.call(context.Background(), boxControlTimeout, "exec", b.container, "rm", "-f", "/tmp/.run-"+runID)
		}
		if err := ctx.Err(); err != nil {
			return BoxExecResult{}, err
		}
		// 容器中途没了（被重置、Docker 重启）：docker exec 自己报错，重建一次再跑。
		if res.code != 0 && boxExecInfraFailure(res.stderr) {
			if attempt == 0 {
				b.lifecycle.Lock()
				b.firewalled = false
				b.lifecycle.Unlock()
				continue
			}
			return BoxExecResult{}, boxErrorf("工作区容器不可用：%s", strings.TrimSpace(res.stderr))
		}
		return BoxExecResult{ExitCode: res.code, TimedOut: res.code == 124 || res.code == 137 || res.timedOut, Duration: duration}, nil
	}
}

func (b *WorkBox) killRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), boxControlTimeout)
	defer cancel()
	pidFile := "/tmp/.run-" + runID
	_ = b.call(ctx, boxControlTimeout, "exec", b.container, "/bin/sh", "-c", `p=$(cat "$1" 2>/dev/null) && kill -KILL -- "-$p"; rm -f "$1"`, "sh", pidFile)
}

func boxExecInfraFailure(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, marker := range []string{"no such container", "is not running", "cannot connect to the docker daemon", "error during connect"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func randomBoxRunID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}

// NormalizeBoxPath 把模型给的路径规范到 /work 下（= 雨夹雪 normalizeBoxPath）。
func NormalizeBoxPath(p string) (string, error) {
	value := p
	if !strings.HasPrefix(value, "/") {
		value = BoxWorkDir + "/" + value
	}
	if strings.ContainsAny(value, "\x00\n\r") {
		return "", boxErrorf("路径必须在 /work 内")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", boxErrorf("路径必须在 /work 内")
		}
	}
	normalized := path.Clean(value)
	if normalized != BoxWorkDir && !strings.HasPrefix(normalized, BoxWorkDir+"/") {
		return "", boxErrorf("路径必须在 /work 内")
	}
	return normalized, nil
}

// WriteFile 把字节写进 box（附件进 /work/inbox 就走这里）。
func (b *WorkBox) WriteFile(ctx context.Context, p string, data io.Reader) (string, error) {
	target, err := NormalizeBoxPath(p)
	if err != nil {
		return "", err
	}
	defer b.use()()
	if err := b.Ensure(ctx); err != nil {
		return "", err
	}
	res := b.docker(ctx, dockerCall{
		args:    []string{"exec", "--interactive", b.container, "/bin/sh", "-c", `mkdir -p -- "$(dirname -- "$1")" && cat > "$1"`, "sh", target},
		stdin:   data,
		timeout: boxControlTimeout,
	})
	if res.code != 0 {
		return "", boxErrorf("写入工作区失败：%s", strings.TrimSpace(res.stderr))
	}
	return target, nil
}

// boxCopyOutScript 先取真实路径再校验仍在 /work 内、是普通文件、不超上限才输出
// （= 雨夹雪 copyOut 的 canonicalPath + kind + size 检查）。
const boxCopyOutScript = `p=$(realpath -e -- "$1") || exit 3
case "$p" in /work|/work/*) ;; *) exit 4;; esac
[ -f "$p" ] && [ ! -L "$p" ] || exit 5
s=$(stat -c %s -- "$p") || exit 5
[ "$s" -le "$2" ] || exit 6
printf '%s\n' "$p"
cat -- "$p"`

// CopyOut 从 box 取出一个有界的普通文件，返回真实路径和内容。
func (b *WorkBox) CopyOut(ctx context.Context, p string, maxBytes int64) (string, []byte, error) {
	target, err := NormalizeBoxPath(p)
	if err != nil {
		return "", nil, err
	}
	defer b.use()()
	if err := b.Ensure(ctx); err != nil {
		return "", nil, err
	}
	var out bytes.Buffer
	limited := &limitedWriter{w: &out, n: maxBytes + int64(len(target)) + 4096}
	res := b.docker(ctx, dockerCall{
		args:    []string{"exec", b.container, "/bin/sh", "-c", boxCopyOutScript, "sh", target, strconv.FormatInt(maxBytes, 10)},
		stdout:  limited,
		timeout: boxControlTimeout,
	})
	switch res.code {
	case 0:
	case 3:
		return "", nil, boxErrorf("工作区里没有 %s", target)
	case 4:
		return "", nil, boxErrorf("路径必须在 /work 内")
	case 5:
		return "", nil, boxErrorf("%s 不是普通文件", target)
	case 6:
		return "", nil, boxErrorf("%s 超过 %d MB", target, maxBytes>>20)
	default:
		return "", nil, boxErrorf("从工作区读取失败：%s", strings.TrimSpace(res.stderr))
	}
	real, data, ok := bytes.Cut(out.Bytes(), []byte("\n"))
	if !ok || limited.overflow || int64(len(data)) > maxBytes {
		return "", nil, boxErrorf("%s 超过 %d MB", target, maxBytes>>20)
	}
	return string(real), data, nil
}

type limitedWriter struct {
	w        io.Writer
	n        int64
	overflow bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		l.overflow = true
		p = p[:max(l.n, 0)]
	}
	l.n -= int64(len(p))
	if _, err := l.w.Write(p); err != nil {
		return 0, err
	}
	if l.overflow {
		return 0, errors.New("output limit exceeded")
	}
	return len(p), nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---- 可用性与 box 登记 --------------------------------------------------------

// boxAvailability 是「这台机器能不能起 work box」的缓存结论。
var boxAvailability struct {
	mu        sync.Mutex
	checked   time.Time
	cfg       BoxConfig
	docker    dockerRunner
	reason    string
	available bool
}

const boxAvailabilityTTL = 30 * time.Second

// boxProbe 返回 docker runner 或者不可用原因；测试里替换它。
var boxProbe = probeBoxRuntime

// probeBoxRuntime 检查 docker CLI 和守护进程，并按平台判断隔离够不够：
//   - Linux（含 Docker 部署、Termux）：照雨夹雪，守护进程必须是 rootless，root 守护进程的
//     socket 等于把宿主 root 交出去；
//   - macOS / Windows：Docker Desktop、colima、OrbStack 的守护进程都在独立虚拟机里，
//     容器逃逸也只到虚拟机，宿主回环由防火墙挡住。
func probeBoxRuntime(cfg BoxConfig) (dockerRunner, string) {
	// 测试进程不碰本机 Docker（不然每周重置会去清开发机上的 box），只有显式要求的集成测试例外。
	if testing.Testing() && os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		return nil, "测试进程不使用 Docker"
	}
	dockerBin, err := lookupDockerBinary()
	if err != nil {
		return nil, "这台机器上找不到 docker CLI"
	}
	docker := execDocker(dockerBin, cfg.DockerHost)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := docker(ctx, dockerCall{args: []string{"info", "--format", "{{json .SecurityOptions}}|{{.OperatingSystem}}"}})
	if res.code != 0 {
		return nil, "连不上 Docker 守护进程：" + strings.TrimSpace(lastLines(res.stderr, 3))
	}
	if reason := boxDaemonPolicy(runtime.GOOS, string(res.stdout)); reason != "" {
		return nil, reason
	}
	return docker, ""
}

func boxDaemonPolicy(goos, info string) string {
	security, osName, _ := strings.Cut(strings.TrimSpace(info), "|")
	if strings.Contains(security, "name=rootless") {
		return ""
	}
	if goos == "darwin" || goos == "windows" || strings.Contains(osName, "Docker Desktop") {
		return ""
	}
	return "Linux 上工作区容器只用 rootless Docker（见 docs/workbox.md），当前守护进程以 root 运行"
}

// BoxStatus 报告 work box 能不能用，不可用时给原因。
func BoxStatus() (bool, string) {
	_, reason := boxRuntime()
	return reason == "", reason
}

func boxRuntime() (dockerRunner, string) {
	boxAvailability.mu.Lock()
	defer boxAvailability.mu.Unlock()
	cfg := BoxConfigFromEnv()
	if time.Since(boxAvailability.checked) < boxAvailabilityTTL && boxAvailability.cfg == cfg {
		return boxAvailability.docker, boxAvailability.reason
	}
	docker, reason := boxProbe(cfg)
	boxAvailability.checked, boxAvailability.cfg, boxAvailability.docker, boxAvailability.reason = time.Now(), cfg, docker, reason
	return docker, reason
}

var boxRegistry sync.Map // key → *WorkBox

// BoxFor 返回 key 对应的 work box；docker 不可用时报错。
func BoxFor(key string) (*WorkBox, error) {
	docker, reason := boxRuntime()
	if reason != "" {
		return nil, boxErrorf("工作区容器不可用：%s", reason)
	}
	cfg := BoxConfigFromEnv()
	if value, ok := boxRegistry.Load(key); ok {
		existing := value.(*WorkBox)
		if existing.cfg == cfg {
			return existing, nil
		}
	}
	created := newWorkBox(cfg, docker, key)
	boxRegistry.Store(key, created)
	return created, nil
}

// ---- 空闲回收 --------------------------------------------------------------------

// BoxIdleTimeout 是容器空闲多久后停掉（删容器、留卷）。
const BoxIdleTimeout = 15 * time.Minute

// StopIdleBoxes 停掉空闲超过 idle 的容器，卷保留，下次使用时重建。本进程登记过的
// box 按最近使用时间判断；别的进程（上一次运行）留下的容器不知道用过没有，按容器
// 启动时间判断，并且容器里还有命令在跑（/tmp/.run-* 在）时不动。
func StopIdleBoxes(ctx context.Context, now time.Time, idle time.Duration) {
	docker, reason := boxRuntime()
	if reason != "" {
		return
	}
	stopIdleBoxes(ctx, docker, now, idle)
}

func stopIdleBoxes(ctx context.Context, docker dockerRunner, now time.Time, idle time.Duration) {
	res := docker(ctx, dockerCall{args: []string{"ps", "--filter", "label=" + boxLabel + "=1", "--format", "{{.Names}}"}, timeout: boxControlTimeout})
	if res.code != 0 {
		return
	}
	for _, name := range strings.Fields(string(res.stdout)) {
		info := docker(ctx, dockerCall{args: []string{"inspect", "--format", "{{.State.StartedAt}}|{{index .Config.Labels \"" + boxKeyLabel + "\"}}", name}, timeout: boxControlTimeout})
		if info.code != 0 {
			continue
		}
		startedText, keyHex, _ := strings.Cut(strings.TrimSpace(string(info.stdout)), "|")
		key, err := hex.DecodeString(keyHex)
		if err != nil || len(key) == 0 {
			continue
		}
		if value, ok := boxRegistry.Load(string(key)); ok {
			b := value.(*WorkBox)
			if b.container != name || b.Busy() || now.Sub(time.Unix(0, b.lastUsed.Load())) < idle {
				continue
			}
			b.Stop(ctx)
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, startedText)
		if err != nil || now.Sub(started) < idle {
			continue
		}
		if docker(ctx, dockerCall{args: []string{"exec", name, "/bin/sh", "-c", `ls /tmp/.run-* >/dev/null 2>&1`}, timeout: boxControlTimeout}).code == 0 {
			continue
		}
		_ = docker(ctx, dockerCall{args: []string{"rm", "--force", name}, timeout: boxControlTimeout})
	}
}

// ---- 每周重置（= 雨夹雪 src/sandbox/schedule.ts）--------------------------------

// BoxResetCheckInterval 是每周重置的检查间隔。
const BoxResetCheckInterval = 10 * time.Minute

const (
	boxResetWeekday = time.Monday
	boxResetHour    = 5
)

// lastWeeklyBoxBoundary 是 now 之前（含）最近的周一 05:00（本机时区）。
func lastWeeklyBoxBoundary(now time.Time) time.Time {
	boundary := time.Date(now.Year(), now.Month(), now.Day(), boxResetHour, 0, 0, 0, now.Location())
	daysBack := (int(boundary.Weekday()) - int(boxResetWeekday) + 7) % 7
	boundary = boundary.AddDate(0, 0, -daysBack)
	if boundary.After(now) {
		boundary = boundary.AddDate(0, 0, -7)
	}
	return boundary
}

// RunWeeklyBoxReset 清空上次清空早于最近一个周一 05:00 的 box。上次清空的时间就是卷的
// 创建时间（重置会删卷，下次使用重建），不另存状态；忙着的 box 等下一次检查。
func RunWeeklyBoxReset(ctx context.Context, now time.Time) {
	docker, reason := boxRuntime()
	if reason != "" {
		return
	}
	res := docker(ctx, dockerCall{args: []string{"volume", "ls", "--filter", "label=" + boxLabel + "=1", "--format", "{{.Name}}"}, timeout: boxControlTimeout})
	if res.code != 0 {
		return
	}
	boundary := lastWeeklyBoxBoundary(now)
	for _, name := range strings.Fields(string(res.stdout)) {
		info := docker(ctx, dockerCall{args: []string{"volume", "inspect", "--format", "{{.CreatedAt}}|{{index .Labels \"" + boxKeyLabel + "\"}}", name}, timeout: boxControlTimeout})
		if info.code != 0 {
			continue
		}
		createdText, keyHex, _ := strings.Cut(strings.TrimSpace(string(info.stdout)), "|")
		created, err := time.Parse(time.RFC3339, createdText)
		key, keyErr := hex.DecodeString(keyHex)
		if err != nil || keyErr != nil || !created.Before(boundary) || len(key) == 0 {
			continue
		}
		workBox, err := BoxFor(string(key))
		if err != nil || workBox.volume != name || workBox.Busy() {
			continue
		}
		_ = workBox.Reset(ctx)
	}
}

// ---- docker 客户端 ----------------------------------------------------------------

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
