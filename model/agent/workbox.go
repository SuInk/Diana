// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
// 根文件系统只读、只留建范围目录要的三项能力、no-new-privileges、限内存/CPU/进程数。
// 容器里没有任何机密，最坏也只是把自己的范围目录弄乱，box_reset 一下就回到干净状态。
//
// 所有会话共用这一个容器：每个会话范围（机器人 × 平台 × 群或私聊对象）一个目录
// /work/<scope>、一个 uid，目录 0700，见 WorkBox。以前每个会话一个容器、一个卷，
// 每个会话第一次用都要冷启动，跨会话拿文件也要来回倒卷；旧卷由 MigrateLegacyBoxes 并进来。
//
// 和雨夹雪的一处差别：联网时 box 接在专用 bridge 网络上，并由一次性的防火墙容器在
// box 的网络命名空间里丢掉发往宿主机、私网和 Tailscale 的包。雨夹雪跑在 Linux
// rootless Docker 上，slirp4netns 默认就够不到宿主回环；Docker Desktop 的容器却能经
// host.docker.internal 直达宿主机 127.0.0.1 上的各种服务，所以这一层必须补上。

const (
	BoxWorkDir = "/work"
	// 范围目录里的固定子目录：inbox 放聊天附件（= 雨夹雪 tg_download 的 /work/inbox），
	// .output 存命令的完整输出（结果放不下时模型按行读它），.home 是 HOME（pip/npm/uv
	// 的用户安装和缓存），tmp 是 TMPDIR。
	boxInboxDir  = "inbox"
	boxOutputDir = ".output"
	boxHomeDir   = ".home"
	boxTmpDir    = "tmp"

	// 共享容器和卷的名字；旧版每会话一个时名字后面带哈希。
	boxContainerName = "diana-box"
	boxVolumeName    = "diana-work"

	boxNetworkName = "diana-box"
	boxLabel       = "diana.box"
	// boxLayoutLabel 标出容器和卷是新布局（共享容器、按范围分目录）。
	boxLayoutLabel  = "diana.box.layout"
	boxLayoutScopes = "scopes"

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
		cfg.Memory = "2g"
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
//
// 和雨夹雪的差别：所有会话共用一个容器，每个会话范围用自己的 uid、自己的 0700 目录
// （见 WorkBox）。建范围目录、把它交给那个 uid 要 root 带 CHOWN/DAC_OVERRIDE/FOWNER，
// 所以容器的能力边界里留这三项；只有 Diana 自己以 --user 0 进去时用得上。命令都以
// 范围 uid 运行，Docker 不给非 root 用户任何能力，no-new-privileges 又挡住 setuid，
// 这三项到不了命令手里。HOME 等环境变量按范围在每次 exec 时给。
func boxIsolationArgs(network bool, volume string) []string {
	netMode := "none"
	if network {
		netMode = boxNetworkName
	}
	return []string{
		"--user", "1000:1000",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--network", netMode,
		"--volume", volume + ":" + BoxWorkDir,
	}
}

// box 出站只放行公网。拒绝的目标：私网、宿主机（Docker Desktop 的 host.docker.internal
// 落在 192.168.65.0/24 或 fdc4::/16）、Tailscale 100.64.0.0/10、链路本地、组播，再加上
// 宿主机自己的网卡地址（公网 IP 也算，见 boxHostAddresses）。回环放行：Docker 内置 DNS
// 127.0.0.11 走它。box 自己没有 NET_ADMIN，改不了这些规则。
var (
	boxRejectV4 = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4"}
	boxRejectV6 = []string{"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "fc00::/7", "fe80::/10", "ff00::/8"}
)

// boxFirewallScript 用 iptables-restore 一次性替换整张 filter 表：新规则要么整体生效，
// 要么不生效，不会像「先清空再逐条追加」那样有一段出站全开的窗口。nat 表不动，Docker
// 内置 DNS 的转发规则在那里。
//
// IPv6：box 的网络命名空间里没有 IPv6 就不写；有 IPv6 但宿主内核没有 ip6tables 能用的表
// 时退出码 7，Diana 用 --sysctl 关掉容器里的 IPv6 重建，见 boxIPv6Off。
const boxFirewallScript = `set -e
PATH=/usr/sbin:/sbin:$PATH
printf '%s' "$1" | iptables-restore -w
if [ -d /proc/sys/net/ipv6 ] && [ "$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6)" != 1 ]; then
  printf '%s' "$2" | ip6tables-restore -w || exit 7
fi
`

// boxFirewallNoIPv6Tables 是防火墙脚本报「宿主不支持 IPv6 表」的退出码。
const boxFirewallNoIPv6Tables = 7

// boxIPv6Off 记下这台宿主写不了 IPv6 规则：之后起的 box 都用 sysctl 关掉 IPv6，出站
// 只剩 IPv4，规则就不再依赖 ip6tables。进程级事实，不会中途变好。
var boxIPv6Off atomic.Bool

var errBoxNoIPv6Tables = errors.New("宿主内核不支持 IPv6 防火墙表")

// boxFirewallRules 生成 iptables-restore 的输入。
func boxFirewallRules(rejects []string) string {
	var b strings.Builder
	b.WriteString("*filter\n:INPUT ACCEPT [0:0]\n:FORWARD ACCEPT [0:0]\n:OUTPUT ACCEPT [0:0]\n-A OUTPUT -o lo -j ACCEPT\n")
	for _, cidr := range rejects {
		b.WriteString("-A OUTPUT -d " + cidr + " -j REJECT\n")
	}
	b.WriteString("COMMIT\n")
	return b.String()
}

// boxHostAddresses 是宿主机网卡上的非回环地址，按 /32、/128 加进拒绝名单：宿主机的
// 公网 IP 不在私网段里，不单独拦的话 box 能经它访问宿主机上监听 0.0.0.0 的服务。
// Diana 自己跑在容器里时看到的是容器的网卡，拿不到宿主地址，见 docs/workbox.md。
var boxHostAddresses = func() (v4, v6 []string) {
	if isRunningInsideContainer() {
		return nil, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, nil
	}
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil || prefix.Addr().IsLoopback() {
			continue
		}
		ip := prefix.Addr().Unmap()
		if ip.Is4() {
			v4 = append(v4, ip.String()+"/32")
		} else {
			v6 = append(v6, ip.WithZone("").String()+"/128")
		}
	}
	return v4, v6
}

// isRunningInsideContainer 判断 Diana 自己是不是跑在容器里。
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

// boxContainer 是所有会话共用的那一个容器和它的卷。生命周期（建、复用判断、防火墙、
// 空闲回收）都在这里；按会话范围分的东西在 WorkBox。
type boxContainer struct {
	cfg       BoxConfig
	docker    dockerRunner
	container string
	volume    string

	// lifecycle 串行化 ensure / stop / 范围目录准备（= 雨夹雪的 exclusive）。
	lifecycle sync.Mutex
	// firewalledFor 是写防火墙时容器的「ID|启动时间」。规则活在容器的网络命名空间里，
	// 容器被外部重启后就没了：复用前按 inspect 比对，对不上就重写。新进程接手旧容器时
	// 它是空的，同样会重写一遍。
	firewalledFor string
	// identity 是当前容器的「ID|启动时间」；scopesReady 记着哪些范围目录已经在这个容器
	// 里建好，容器换了就重新准备。
	identity    string
	scopesReady map[string]string
	running     atomic.Int32
	// lastUsed 是任何范围最近一次用容器的时间（UnixNano），空闲回收按它判断。
	lastUsed atomic.Int64
}

func newBoxContainer(cfg BoxConfig, docker dockerRunner) *boxContainer {
	return &boxContainer{cfg: cfg, docker: docker, container: boxContainerName, volume: boxVolumeName, scopesReady: map[string]string{}}
}

// WorkBox 是共享容器里的一个会话范围：自己的目录 /work/<scope>、自己的 uid。
//
// 同一个容器里，范围之间靠内核权限隔开：目录 0700 归范围 uid，/work 本身 0711 归
// root（进得去、列不出、建不了东西），命令只以范围 uid 运行。文件工具、send_file
// 也都以这个 uid 读写，所以一个范围看不到另一个范围的任何文件。
type WorkBox struct {
	host  *boxContainer
	key   string
	scope string
	uid   int

	// diskLimit 是这个范围目录的上限（字节），见 SetDiskLimit。
	diskLimit atomic.Int64
}

// boxScopeName 把会话 key 变成范围目录名。/work 列不出目录，名字不必可读。
func boxScopeName(key string) string {
	return "s-" + boxNameFor(key)
}

// boxNameFor 把任意 key 变成名字安全的后缀。
func boxNameFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// Root 是这个范围在容器里的目录，命令的当前目录。
func (b *WorkBox) Root() string { return BoxWorkDir + "/" + b.scope }

func (b *WorkBox) uidArg() string {
	uid := strconv.Itoa(b.uid)
	return uid + ":" + uid
}

// scopeEnv 是这个范围每次 exec 带的环境变量：pip/npm/uv 的用户安装和缓存都落在范围
// 自己的 .home，临时文件落在自己的 tmp，不和别的范围共用。
func (b *WorkBox) scopeEnv() []string {
	root := b.Root()
	home := root + "/" + boxHomeDir
	return []string{
		"HOME=" + home,
		"TMPDIR=" + root + "/" + boxTmpDir,
		"UV_CACHE_DIR=" + home + "/.cache/uv",
		"NPM_CONFIG_PREFIX=" + home + "/.npm-global",
		"PATH=" + home + "/.local/bin:" + home + "/.npm-global/bin:/usr/local/bin:/usr/bin:/bin",
	}
}

// execArgs 是以范围 uid、在范围目录里 docker exec 的前半段，后面接要跑的程序。
func (b *WorkBox) execArgs(interactive bool) []string {
	args := []string{"exec"}
	if interactive {
		args = append(args, "--interactive")
	}
	args = append(args, "--user", b.uidArg(), "--workdir", b.Root())
	for _, env := range b.scopeEnv() {
		args = append(args, "--env", env)
	}
	return append(args, b.host.container)
}

// scopeProcLimit 是一个范围同时能有的进程数（RLIMIT_NPROC 按 uid 计）：一个范围的
// fork 炸弹或失控的并发只占满自己那一份，不把整个容器的 pids 上限吃光。
func (b *WorkBox) scopeProcLimit() int {
	return max(b.host.cfg.PIDs/2, 16)
}

func (b *boxContainer) call(ctx context.Context, timeout time.Duration, args ...string) dockerResult {
	return b.docker(ctx, dockerCall{args: args, timeout: timeout})
}

func (b *boxContainer) runArgs() []string {
	args := []string{
		"run", "--detach",
		"--name", b.container,
		"--hostname", "box",
		// PID 1 若是 sleep，超时被杀的后台进程会变成收不掉的僵尸，慢慢占满 pids 上限；
		// --init 让 tini 回收它们。
		"--init",
		"--label", boxLabel + "=1",
		"--label", boxLayoutLabel + "=" + boxLayoutScopes,
	}
	args = append(args, boxIsolationArgs(b.cfg.Network, b.volume)...)
	if b.cfg.Network && boxIPv6Off.Load() {
		args = append(args, "--sysctl", "net.ipv6.conf.all.disable_ipv6=1", "--sysctl", "net.ipv6.conf.default.disable_ipv6=1")
	}
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

func (b *boxContainer) firewallArgs() []string {
	hostV4, hostV6 := boxHostAddresses()
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
		"-c", boxFirewallScript, "sh",
		boxFirewallRules(append(append([]string(nil), boxRejectV4...), hostV4...)),
		boxFirewallRules(append(append([]string(nil), boxRejectV6...), hostV6...)),
	}
}

// Ensure 确保容器在跑、镜像对、防火墙已写（= 雨夹雪 ensure/start）。
func (b *boxContainer) Ensure(ctx context.Context) error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	return b.start(ctx)
}

// boxInspectFormat 一次取出复用判断要的全部字段。
const boxInspectFormat = "{{.State.Running}} {{.Config.Image}} {{.Id}} {{.State.StartedAt}} {{.HostConfig.NetworkMode}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}} {{json .HostConfig.CapAdd}}"

type boxContainerState struct {
	exists, running bool
	image, identity string
	// shape 是「网络模式 内存字节 NanoCpus 进程数 额外能力」，和 wantShape 不一致就重建。
	shape string
}

func (b *boxContainer) inspect(ctx context.Context) boxContainerState {
	res := b.call(ctx, boxControlTimeout, "inspect", "--format", boxInspectFormat, b.container)
	if res.code != 0 {
		return boxContainerState{}
	}
	fields := strings.Fields(string(res.stdout))
	if len(fields) != 9 {
		return boxContainerState{exists: true}
	}
	return boxContainerState{exists: true, running: fields[0] == "true", image: fields[1], identity: fields[2] + "|" + fields[3], shape: strings.Join(fields[4:], " ")}
}

// boxCapAddJSON 是 inspect 里 HostConfig.CapAdd 应有的样子，顺序和 boxIsolationArgs 一致。
const boxCapAddJSON = `["CHOWN","DAC_OVERRIDE","FOWNER"]`

// wantShape 是按当前配置起出来的容器在 inspect 里的样子。部署方改了网络开关或资源
// 上限，旧容器就对不上：尤其是从断网改成联网，旧的 --network none 容器不能带着
// 「没写防火墙」的状态继续用，反过来也一样。
func (b *boxContainer) wantShape() string {
	network := "none"
	if b.cfg.Network {
		network = boxNetworkName
	}
	memory, ok := parseBoxMemory(b.cfg.Memory)
	memoryText := strconv.FormatInt(memory, 10)
	if !ok {
		memoryText = "?"
	}
	return strings.Join([]string{network, memoryText, strconv.FormatInt(int64(math.Round(b.cfg.CPUs*1e9)), 10), strconv.Itoa(b.cfg.PIDs), boxCapAddJSON}, " ")
}

// parseBoxMemory 按 docker --memory 的规则解析：数字加可选的 b/k/m/g 单位，1024 进制。
func parseBoxMemory(value string) (int64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	unit := int64(1)
	switch {
	case strings.HasSuffix(value, "g"):
		unit, value = 1<<30, strings.TrimSuffix(value, "g")
	case strings.HasSuffix(value, "m"):
		unit, value = 1<<20, strings.TrimSuffix(value, "m")
	case strings.HasSuffix(value, "k"):
		unit, value = 1<<10, strings.TrimSuffix(value, "k")
	case strings.HasSuffix(value, "b"):
		value = strings.TrimSuffix(value, "b")
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int64(n * float64(unit)), true
}

func (b *boxContainer) start(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		err := b.startOnce(ctx)
		if !errors.Is(err, errBoxNoIPv6Tables) || attempt > 0 {
			return err
		}
		// 宿主写不了 IPv6 规则：关掉容器里的 IPv6 重建一次。
		boxIPv6Off.Store(true)
	}
}

// boxRootInitScript 把 /work 交给 root、设成 0711：各范围 uid 进得去自己的目录，
// 但列不出别人的目录名，也不能在 /work 下直接建文件。
const boxRootInitScript = `chown 0:0 /work && chmod 0711 /work`

func (b *boxContainer) startOnce(ctx context.Context) error {
	state := b.inspect(ctx)
	if state.running && state.image == b.cfg.Image && state.shape == b.wantShape() {
		if state.identity != b.identity {
			// 上一个进程起的容器，或者被外部重启过：范围目录照旧在卷里，但要按新容器重新核对一遍。
			b.identity, b.scopesReady = state.identity, map[string]string{}
		}
		if !b.cfg.Network || b.firewalledFor == state.identity {
			return nil
		}
		if err := b.applyFirewall(ctx, state.identity); err != nil {
			_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
			return err
		}
		return nil
	}
	b.firewalledFor, b.identity, b.scopesReady = "", "", map[string]string{}
	if state.exists {
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
	if res := b.call(ctx, boxControlTimeout, "volume", "create", "--label", boxLabel+"=1", "--label", boxLayoutLabel+"="+boxLayoutScopes, b.volume); res.code != 0 {
		return boxErrorf("工作区卷创建失败：%s", strings.TrimSpace(res.stderr))
	}
	if res := b.call(ctx, boxControlTimeout, b.runArgs()...); res.code != 0 {
		return boxErrorf("工作区容器启动失败：%s", strings.TrimSpace(res.stderr))
	}
	started := b.inspect(ctx)
	if b.cfg.Network {
		// 防火墙在任何别的东西进容器之前写上；没写上的联网容器不能留着。
		if err := b.applyFirewall(ctx, started.identity); err != nil {
			_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
			return err
		}
	}
	if res := b.call(ctx, boxControlTimeout, "exec", "--user", "0:0", b.container, "/bin/sh", "-c", boxRootInitScript); res.code != 0 {
		_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
		return boxErrorf("工作区初始化失败：%s", strings.TrimSpace(res.stderr))
	}
	b.identity = started.identity
	return nil
}

// boxScopeDirScript 以 root 建范围目录并交给范围 uid。只动 /work 下这一层：/work 归 root、
// 别人写不进，目录本身不会被换成软链接；目录里面由范围 uid 自己建（boxScopeSkeletonScript），
// root 不碰范围 uid 能控制的任何路径。
const boxScopeDirScript = `d="/work/$1"
[ -L "$d" ] && exit 4
mkdir -p -- "$d" && chown "$2:$2" -- "$d" && chmod 0700 -- "$d"`

// boxScopeSkeletonScript 以范围 uid 建范围目录里的固定子目录。
const boxScopeSkeletonScript = `mkdir -p -- "$1/` + boxHomeDir + `" "$1/` + boxInboxDir + `" "$1/` + boxOutputDir + `" "$1/` + boxTmpDir + `"`

// Ensure 确保共享容器在跑，并且这个范围的目录已经建好、归范围 uid。
func (b *WorkBox) Ensure(ctx context.Context) error {
	host := b.host
	host.lifecycle.Lock()
	defer host.lifecycle.Unlock()
	if err := host.start(ctx); err != nil {
		return err
	}
	if host.identity != "" && host.scopesReady[b.scope] == host.identity {
		return nil
	}
	if res := host.call(ctx, boxControlTimeout, "exec", "--user", "0:0", host.container, "/bin/sh", "-c", boxScopeDirScript, "sh", b.scope, strconv.Itoa(b.uid)); res.code != 0 {
		return boxErrorf("工作区目录准备失败：%s", strings.TrimSpace(res.stderr))
	}
	if res := host.call(ctx, boxControlTimeout, append(b.execArgs(false), "/bin/sh", "-c", boxScopeSkeletonScript, "sh", b.Root())...); res.code != 0 {
		return boxErrorf("工作区目录准备失败：%s", strings.TrimSpace(res.stderr))
	}
	host.scopesReady[b.scope] = host.identity
	return nil
}

func (b *boxContainer) applyFirewall(ctx context.Context, identity string) error {
	if identity == "" {
		return boxErrorf("工作区容器状态读取失败，没有写防火墙")
	}
	res := b.call(ctx, boxControlTimeout, b.firewallArgs()...)
	if res.code == boxFirewallNoIPv6Tables {
		return errBoxNoIPv6Tables
	}
	if res.code != 0 {
		return boxErrorf("工作区出站防火墙写入失败：%s", strings.TrimSpace(res.stderr))
	}
	b.firewalledFor = identity
	return nil
}

// boxBuildMu 防止多个 box 同时构建同一个镜像。
var boxBuildMu sync.Mutex

func (b *boxContainer) ensureImage(ctx context.Context) error {
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

func (b *boxContainer) ensureNetwork(ctx context.Context) error {
	if b.call(ctx, boxControlTimeout, "network", "inspect", "--format", "{{.Id}}", boxNetworkName).code == 0 {
		return nil
	}
	res := b.call(ctx, boxControlTimeout, "network", "create", "--driver", "bridge", "--label", boxLabel+"=1", boxNetworkName)
	if res.code != 0 && !strings.Contains(res.stderr, "already exists") {
		return boxErrorf("工作区网络创建失败：%s", strings.TrimSpace(res.stderr))
	}
	return nil
}

// boxScopeKillArgs 停掉这个范围 uid 的所有进程。pkill 直接作为 exec 的程序运行，
// 不套 shell：pkill 不会杀自己，套一层 sh 的话那层 sh 会被一起杀掉。
func (b *WorkBox) boxScopeKillArgs() []string {
	return append(b.execArgs(false), "pkill", "-KILL", "-U", strconv.Itoa(b.uid))
}

// boxScopeClearScript 以范围 uid 清空范围目录。先补上自己的读写权限，免得命令留下的
// 0000 目录删不掉。
const boxScopeClearScript = `chmod -R u+rwX -- "$1" 2>/dev/null
find "$1" -mindepth 1 -delete`

// Reset 清空这个范围（= 雨夹雪 reset）：停掉它的进程、删光它的目录。别的范围和共享
// 容器不受影响；下次使用时重建目录骨架。
func (b *WorkBox) Reset(ctx context.Context) error {
	host := b.host
	host.lifecycle.Lock()
	defer host.lifecycle.Unlock()
	if err := host.start(ctx); err != nil {
		return err
	}
	// 范围目录还没建过（这个容器里没有这个范围）时没有东西可清。
	if res := host.call(ctx, boxControlTimeout, "exec", "--user", b.uidArg(), host.container, "test", "-d", b.Root()); res.code != 0 {
		delete(host.scopesReady, b.scope)
		return nil
	}
	_ = host.call(ctx, boxControlTimeout, b.boxScopeKillArgs()...)
	if res := host.call(ctx, boxControlTimeout, append(b.execArgs(false), "/bin/sh", "-c", boxScopeClearScript, "sh", b.Root())...); res.code != 0 {
		return boxErrorf("工作区清空失败：%s", strings.TrimSpace(res.stderr))
	}
	delete(host.scopesReady, b.scope)
	return nil
}

// use 标记一次使用的开始；返回的函数标记结束。两头都刷新共享容器的 lastUsed：
// 空闲回收看的是所有范围加起来。
func (b *WorkBox) use() func() {
	b.host.running.Add(1)
	b.host.lastUsed.Store(time.Now().UnixNano())
	return func() {
		b.host.lastUsed.Store(time.Now().UnixNano())
		b.host.running.Add(-1)
	}
}

// idleLocked 在持有 lifecycle 时复查没有操作在进行、且最近一次使用早于 now-idle。
// 回收方先在锁外粗筛，真正动手前必须在锁内再看一遍：粗筛之后可能有命令刚开始。
func (b *boxContainer) idleLocked(now time.Time, idle time.Duration) bool {
	return b.running.Load() == 0 && now.Sub(time.Unix(0, b.lastUsed.Load())) >= idle
}

// StopIfIdle 删掉容器、保留卷：所有范围都空闲时回收，下次使用时重建，/work 里的东西还在。
func (b *boxContainer) StopIfIdle(ctx context.Context, now time.Time, idle time.Duration) bool {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	if !b.idleLocked(now, idle) {
		return false
	}
	_ = b.call(ctx, boxControlTimeout, "rm", "--force", b.container)
	b.firewalledFor, b.identity, b.scopesReady = "", "", map[string]string{}
	return true
}

// BoxExecResult 是一次命令的结果；合并的 stdout/stderr 在容器里的 OutputPath。
type BoxExecResult struct {
	ExitCode   int
	TimedOut   bool
	Duration   time.Duration
	OutputPath string
	// DiskFull 不为空时，这条命令因为范围目录超过磁盘上限没有运行（Refused）或被停掉。
	DiskFull *BoxDiskUsage
}

// BoxDiskUsage 是范围目录的占用和上限，字节。
type BoxDiskUsage struct {
	UsedBytes  int64
	LimitBytes int64
	// Refused 表示命令还没开始就被拒绝：开始前已经超过上限。
	Refused bool
}

// DefaultBoxDiskLimitBytes 是每个会话范围目录的默认上限。
const DefaultBoxDiskLimitBytes = 5 << 30

// boxDiskCheckInterval 是命令运行中检查范围目录占用的间隔（秒）。
const boxDiskCheckInterval = 3

// boxDiskRefusedExit 是范围目录已经超限、命令或写入没有进行时的退出码（EX_TEMPFAIL）。
const boxDiskRefusedExit = 75

// SetDiskLimit 设置这个范围目录的上限（字节），≤0 按默认值。范围按「机器人 × 会话」
// 分，上限是机器人设置，每次使用前由调用方按当前设置刷新。
func (b *WorkBox) SetDiskLimit(limit int64) {
	if limit <= 0 {
		limit = DefaultBoxDiskLimitBytes
	}
	b.diskLimit.Store(limit)
}

func (b *WorkBox) diskLimitBytes() int64 {
	if limit := b.diskLimit.Load(); limit > 0 {
		return limit
	}
	return DefaultBoxDiskLimitBytes
}

// boxOutputKeep 是范围目录 .output 里最多留几份完整输出，更早的在下一条命令前删掉。
const boxOutputKeep = 20

// boxExecScript 先轮转 $8/.output，再让 timeout 成为进程组长（GNU timeout 会 setpgid）
// 并记下它的 pid：超时由 timeout 自己杀整组；调用方取消时 Diana 照 pid 杀组。docker exec
// 客户端被杀并不会结束容器里的进程，这一步对应雨夹雪里 pi-env 的取消。输出直接写进
// 容器里的文件，宿主机这边事后只读尾部窗口。
//
// 输出文件名以 runID 结尾；轮转时跳过 /tmp/.run-<runID> 还在的那些，正在跑的命令的
// 日志不会被删。
//
// 磁盘上限（$6，KiB）：Docker 的 local 卷驱动没有通用的大小配额，所以在容器里自己管，
// 按范围目录（$8）算。开始前 du 一次，已经超限就不跑（写 /tmp/.diskfull-<runID>、退出
// 75）；运行中后台每 $7 秒 du 一次，超限就杀掉整个进程组并留下同样的标记；ulimit -f
// 再兜住单个文件（dash 的单位是 512 字节）。.output 在范围目录里，一起算。后台检查不能
// 继承 docker exec 的 stdout：继承了的话客户端要等它关掉才返回，每条命令都会拖到超时。
// 进程数（$9）按 uid 限（dash 的 ulimit -p，bash 叫 -u）。
const boxExecScript = `mkdir -p "$8/.output"
ls -1t "$8"/.output/*.log 2>/dev/null | tail -n +$5 | while IFS= read -r f; do
  id=${f##*-}; id=${id%.log}
  [ -e "/tmp/.run-$id" ] || rm -f -- "$f"
done
used=$(du -sxk "$8" 2>/dev/null | cut -f1)
if [ "${used:-0}" -ge "$6" ]; then echo "$used" > "/tmp/.diskfull-$1"; exit 75; fi
echo $$ > "/tmp/.run-$1" || exit 1
( while sleep "$7"; do
    [ -e "/tmp/.run-$1" ] || exit 0
    used=$(du -sxk "$8" 2>/dev/null | cut -f1)
    if [ "${used:-0}" -gt "$6" ]; then
      echo "$used" > "/tmp/.diskfull-$1"
      env kill -KILL -- "-$(cat "/tmp/.run-$1")" 2>/dev/null
      exit 0
    fi
  done ) </dev/null >/dev/null 2>&1 &
ulimit -f $(( $6 * 2 ))
ulimit -p "$9" 2>/dev/null || ulimit -u "$9" 2>/dev/null
exec timeout --kill-after=5 "$2" bash -c "$3" > "$4" 2>&1`

// boxExecCleanupScript 删掉 pid 文件（后台检查随之退出），并把磁盘超限标记里的 KiB 数
// 打出来（没有就什么都不打）。
const boxExecCleanupScript = `cat "/tmp/.diskfull-$1" 2>/dev/null; rm -f "/tmp/.run-$1" "/tmp/.diskfull-$1"`

// Exec 以范围 uid、在范围目录里用 bash 跑 command。
func (b *WorkBox) Exec(ctx context.Context, command string, timeout time.Duration) (BoxExecResult, error) {
	if timeout <= 0 {
		timeout = BoxCommandDefaultTimeout
	}
	timeout = min(timeout, BoxCommandMaxTimeout)
	limit := b.diskLimitBytes()
	limitKiB := max(limit>>10, 1)
	defer b.use()()
	for attempt := 0; ; attempt++ {
		if err := b.Ensure(ctx); err != nil {
			return BoxExecResult{}, err
		}
		runID := randomBoxRunID()
		outputPath := fmt.Sprintf("%s/%s/%s-%s.log", b.Root(), boxOutputDir, time.Now().Format("20060102-150405"), runID)
		start := time.Now()
		res := b.host.docker(ctx, dockerCall{
			args: append(b.execArgs(false), "/bin/sh", "-c", boxExecScript, "sh", runID, strconv.Itoa(int(timeout/time.Second)), command, outputPath,
				strconv.Itoa(boxOutputKeep), strconv.FormatInt(limitKiB, 10), strconv.Itoa(boxDiskCheckInterval), b.Root(), strconv.Itoa(b.scopeProcLimit())),
			timeout: timeout + 15*time.Second,
		})
		duration := time.Since(start)
		if ctx.Err() != nil || res.timedOut {
			b.killRun(runID)
		}
		cleanup := b.host.call(context.Background(), boxControlTimeout, append(b.execArgs(false), "/bin/sh", "-c", boxExecCleanupScript, "sh", runID)...)
		if err := ctx.Err(); err != nil {
			return BoxExecResult{}, err
		}
		// 容器中途没了（被重建、Docker 重启）：docker exec 自己报错，重建一次再跑。
		if res.code != 0 && boxExecInfraFailure(res.stderr) {
			if attempt == 0 {
				continue
			}
			return BoxExecResult{}, boxErrorf("工作区容器不可用：%s", strings.TrimSpace(res.stderr))
		}
		// 124 是 timeout 到点；137 是被 SIGKILL：到点后 TERM 不理、5 秒后被补杀的也是 137，
		// 所以耗时到了超时时间的 137 算超时，没到的（比如内存超限被 OOM 杀）不算。
		result := BoxExecResult{ExitCode: res.code, Duration: duration, OutputPath: outputPath}
		if usedKiB, err := strconv.ParseInt(strings.TrimSpace(string(cleanup.stdout)), 10, 64); err == nil {
			result.DiskFull = &BoxDiskUsage{UsedBytes: usedKiB << 10, LimitBytes: limit, Refused: res.code == boxDiskRefusedExit}
			return result, nil
		}
		result.TimedOut = res.code == 124 || res.timedOut || (res.code == 137 && duration >= timeout)
		return result, nil
	}
}

// boxOutputTailScript 输出「总字节 总行数」一行，再输出文件末尾最多 $2 字节。行数照
// countLines：结尾没有换行的最后一段也算一行。
const boxOutputTailScript = `f="$1"
s=$(stat -c %s -- "$f") || exit 3
l=$(wc -l < "$f")
if [ "$s" -gt 0 ] && [ "$(tail -c 1 -- "$f" | wc -l)" -eq 0 ]; then l=$((l+1)); fi
printf '%s %s\n' "$s" "$l"
tail -c "$2" -- "$f"`

// OutputTail 读范围里一份完整输出的末尾 window 字节，连同它的总字节数、总行数和窗口
// 起点。整份输出不出容器。
func (b *WorkBox) OutputTail(ctx context.Context, p string, window int64) (tail []byte, start, size int64, lines int, err error) {
	target, err := b.ResolvePath(p)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer b.use()()
	var out bytes.Buffer
	limited := &limitedWriter{w: &out, n: window + 64}
	res := b.host.docker(ctx, dockerCall{
		args:    append(b.execArgs(false), "/bin/sh", "-c", boxOutputTailScript, "sh", target, strconv.FormatInt(window, 10)),
		stdout:  limited,
		timeout: boxControlTimeout,
	})
	if res.code != 0 {
		return nil, 0, 0, 0, boxErrorf("读取命令输出失败：%s", strings.TrimSpace(res.stderr))
	}
	header, body, ok := bytes.Cut(out.Bytes(), []byte("\n"))
	if !ok {
		return nil, 0, 0, 0, boxErrorf("读取命令输出失败：格式不对")
	}
	if _, err := fmt.Sscanf(string(header), "%d %d", &size, &lines); err != nil {
		return nil, 0, 0, 0, boxErrorf("读取命令输出失败：%v", err)
	}
	return body, max(size-int64(len(body)), 0), size, lines, nil
}

func (b *WorkBox) killRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), boxControlTimeout)
	defer cancel()
	pidFile := "/tmp/.run-" + runID
	_ = b.host.call(ctx, boxControlTimeout, append(b.execArgs(false), "/bin/sh", "-c", `p=$(cat "$1" 2>/dev/null) && env kill -KILL -- "-$p"; rm -f "$1"`, "sh", pidFile)...)
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

// ResolvePath 把模型给的路径落到这个范围目录里。相对路径按范围目录算；
// /work/<范围>/… 原样用；其他 /work/… 是老习惯（以前每个会话的根就是 /work），按范围
// 目录下的同名路径算。.. 和 /work 以外的绝对路径都不行。
func (b *WorkBox) ResolvePath(p string) (string, error) {
	value := strings.TrimSpace(p)
	if strings.ContainsAny(value, "\x00\n\r") {
		return "", boxErrorf("路径必须在工作区内")
	}
	root := b.Root()
	switch {
	case value == "":
		value = root
	case !strings.HasPrefix(value, "/"):
		value = root + "/" + value
	case value == root || strings.HasPrefix(value, root+"/"):
	case value == BoxWorkDir || strings.HasPrefix(value, BoxWorkDir+"/"):
		value = root + strings.TrimPrefix(value, BoxWorkDir)
	default:
		return "", boxErrorf("路径必须在工作区内")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", boxErrorf("路径必须在工作区内")
		}
	}
	normalized := path.Clean(value)
	if normalized != root && !strings.HasPrefix(normalized, root+"/") {
		return "", boxErrorf("路径必须在工作区内")
	}
	return normalized, nil
}

// WriteFile 把字节写进范围目录（附件进 inbox/ 就走这里），返回容器里的绝对路径。
func (b *WorkBox) WriteFile(ctx context.Context, p string, data io.Reader) (string, error) {
	target, err := b.ResolvePath(p)
	if err != nil {
		return "", err
	}
	defer b.use()()
	if err := b.Ensure(ctx); err != nil {
		return "", err
	}
	limit := b.diskLimitBytes()
	res := b.host.docker(ctx, dockerCall{
		args:    append(b.execArgs(true), "/bin/sh", "-c", boxWriteFileScript, "sh", target, strconv.FormatInt(max(limit>>10, 1), 10), b.Root()),
		stdin:   data,
		timeout: boxControlTimeout,
	})
	if res.code == boxDiskRefusedExit || (res.code != 0 && strings.Contains(strings.ToLower(res.stderr), "file too large")) {
		usage := BoxDiskUsage{LimitBytes: limit, Refused: true}
		if usedKiB, err := strconv.ParseInt(strings.TrimSpace(string(res.stdout)), 10, 64); err == nil {
			usage.UsedBytes = usedKiB << 10
		}
		return "", boxErrorf("%s", usage.Message())
	}
	if res.code != 0 {
		return "", boxErrorf("写入工作区失败：%s", strings.TrimSpace(res.stderr))
	}
	return target, nil
}

// boxWriteFileScript 写文件前看范围目录（$3）还剩多少，用 ulimit -f 把这次写入限制在
// 剩余空间内；已经超限时打出已用 KiB 并退出 75。
const boxWriteFileScript = `used=$(du -sxk "$3" 2>/dev/null | cut -f1)
if [ "${used:-0}" -ge "$2" ]; then echo "$used"; exit 75; fi
mkdir -p -- "$(dirname -- "$1")" || exit 1
ulimit -f $(( ($2 - ${used:-0}) * 2 ))
cat > "$1"`

// Message 是写给模型和用户的超限说明：用了多少、上限多少、怎么清理。
func (u BoxDiskUsage) Message() string {
	used := "超过上限"
	if u.UsedBytes > 0 {
		used = "已用 " + FormatBoxBytes(u.UsedBytes)
	}
	action := "这条命令被停掉了"
	if u.Refused {
		action = "这次没有执行"
	}
	return fmt.Sprintf("本会话的工作区%s，上限 %s，%s。可以用 box_reset（主人发「清空上下文」也会清空）清空工作区，或者先删掉不要的文件（比如 rm -rf 用不上的仓库、.home/.cache）再试。", used, FormatBoxBytes(u.LimitBytes), action)
}

// FormatBoxBytes 把字节数写成人读的 MB / GB。
func FormatBoxBytes(n int64) string {
	if n >= 1<<30 {
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	}
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 0, 64) + " MB"
}

// boxCopyOutScript 先取真实路径再校验仍在范围目录（$3）内、是普通文件、不超上限才输出
// （= 雨夹雪 copyOut 的 canonicalPath + kind + size 检查）。以范围 uid 运行，本来也读
// 不到别的范围；这里再挡住指向范围外的软链接。
const boxCopyOutScript = `p=$(realpath -e -- "$1") || exit 3
case "$p" in "$3"|"$3"/*) ;; *) exit 4;; esac
[ -f "$p" ] && [ ! -L "$p" ] || exit 5
s=$(stat -c %s -- "$p") || exit 5
[ "$s" -le "$2" ] || exit 6
printf '%s\n' "$p"
cat -- "$p"`

// CopyOut 从范围目录取出一个有界的普通文件，返回真实路径和内容。
func (b *WorkBox) CopyOut(ctx context.Context, p string, maxBytes int64) (string, []byte, error) {
	target, err := b.ResolvePath(p)
	if err != nil {
		return "", nil, err
	}
	defer b.use()()
	if err := b.Ensure(ctx); err != nil {
		return "", nil, err
	}
	var out bytes.Buffer
	limited := &limitedWriter{w: &out, n: maxBytes + int64(len(target)) + 4096}
	res := b.host.docker(ctx, dockerCall{
		args:    append(b.execArgs(false), "/bin/sh", "-c", boxCopyOutScript, "sh", target, strconv.FormatInt(maxBytes, 10), b.Root()),
		stdout:  limited,
		timeout: boxControlTimeout,
	})
	switch res.code {
	case 0:
	case 3:
		return "", nil, boxErrorf("工作区里没有 %s", target)
	case 4:
		return "", nil, boxErrorf("路径必须在工作区内")
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

// probeBoxRuntime 找一个能用的 Docker 守护进程：部署方指定了 DIANA_BOX_DOCKER_HOST 就只
// 试它；否则先试 docker CLI 的默认连接（DOCKER_HOST、DOCKER_CONTEXT 或当前上下文），
// Linux 上再试 rootless 的默认 socket $XDG_RUNTIME_DIR/docker.sock。第一个连得上且
// 过得了 boxDaemonPolicy 的胜出；都不行时报第一个的原因。
func probeBoxRuntime(cfg BoxConfig) (dockerRunner, string) {
	// 测试进程不碰本机 Docker（不然空闲回收和旧卷迁移会去动开发机上的 box），只有显式要求的集成测试例外。
	if testing.Testing() && os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		return nil, "测试进程不使用 Docker"
	}
	dockerBin, err := lookupDockerBinary()
	if err != nil {
		return nil, "这台机器上找不到 docker CLI"
	}
	hosts := []string{cfg.DockerHost}
	if cfg.DockerHost == "" {
		if socket := rootlessDockerSocket(); socket != "" {
			hosts = append(hosts, "unix://"+socket)
		}
	}
	firstReason := ""
	for _, host := range hosts {
		docker := execDocker(dockerBin, host)
		reason := checkBoxDaemon(docker, host)
		if reason == "" {
			return docker, ""
		}
		if firstReason == "" {
			firstReason = reason
		}
	}
	return nil, firstReason
}

// rootlessDockerSocket 是 Linux 上 rootless Docker 的默认 socket，不存在时为空。
func rootlessDockerSocket() string {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return ""
	}
	dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socket := dir + "/docker.sock"
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return socket
}

func checkBoxDaemon(docker dockerRunner, host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := docker(ctx, dockerCall{args: []string{"info", "--format", "{{json .SecurityOptions}}|{{.OperatingSystem}}"}})
	if res.code != 0 {
		return "连不上 Docker 守护进程：" + strings.TrimSpace(lastLines(res.stderr, 3))
	}
	if host == "" {
		// 没指定地址时问 CLI 自己实际连的是哪儿：DOCKER_HOST 优先，其次是当前上下文。
		host = strings.TrimSpace(os.Getenv("DOCKER_HOST"))
		if host == "" {
			ctxRes := docker(ctx, dockerCall{args: []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"}})
			host = strings.TrimSpace(string(ctxRes.stdout))
		}
	}
	security, osName, _ := strings.Cut(strings.TrimSpace(string(res.stdout)), "|")
	return boxDaemonPolicy(runtime.GOOS, host, security, osName)
}

// boxDaemonPolicy 判断这个守护进程够不够隔离：
//   - 必须是本机 socket（unix:// 或 Windows 的 npipe://）。远端守护进程上，容器、防火墙
//     和宿主地址都是另一台机器的事，Diana 判断不了；
//   - rootless 守护进程可以（照雨夹雪）；
//   - 否则只认守护进程跑在虚拟机里的情况：macOS、Windows 上的本机 socket 必然通往虚拟机
//     （Docker Desktop、colima、OrbStack），Linux 上只认 Docker Desktop。Linux 原生 root
//     守护进程的 socket 等于宿主 root，不用。
func boxDaemonPolicy(goos, host, security, osName string) string {
	if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return fmt.Sprintf("工作区容器只用本机的 Docker 守护进程，当前连接的是 %q", host)
	}
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

// sharedBox 是本进程唯一的共享容器。部署方改了 DIANA_BOX_* 设置要重启 Diana 才生效：
// 两个实例各拿各的锁，会交错着建容器、写防火墙。
var sharedBox struct {
	mu sync.Mutex
	c  *boxContainer
}

func sharedBoxContainer(docker dockerRunner) *boxContainer {
	sharedBox.mu.Lock()
	defer sharedBox.mu.Unlock()
	if sharedBox.c == nil {
		sharedBox.c = newBoxContainer(BoxConfigFromEnv(), docker)
	}
	return sharedBox.c
}

func registeredBoxContainer() *boxContainer {
	sharedBox.mu.Lock()
	defer sharedBox.mu.Unlock()
	return sharedBox.c
}

var boxRegistry sync.Map // key → *WorkBox

// BoxFor 返回 key 对应的会话范围；docker 不可用时报错。
func BoxFor(key string) (*WorkBox, error) {
	docker, reason := boxRuntime()
	if reason != "" {
		return nil, boxErrorf("工作区容器不可用：%s", reason)
	}
	if value, ok := boxRegistry.Load(key); ok {
		return value.(*WorkBox), nil
	}
	scope := boxScopeName(key)
	uid, err := boxScopes.uidFor(scope)
	if err != nil {
		return nil, err
	}
	value, _ := boxRegistry.LoadOrStore(key, &WorkBox{host: sharedBoxContainer(docker), key: key, scope: scope, uid: uid})
	return value.(*WorkBox), nil
}

// ---- 范围 uid 登记 ----------------------------------------------------------------

// 范围 uid 从 boxScopeUIDBase 起依次分配，一个范围一个、永不复用。登记表放在宿主机
// 的运行时状态目录（.diana/），不放卷里：uid 决定命令能读到哪个目录，放在容器里的
// 话，任何范围都能改它把自己指到别人的 uid 上。
const (
	boxScopeUIDBase  = 10000
	boxScopeUIDLimit = 60000
	boxScopesFile    = "box-scopes.json"
)

type boxScopeUIDs struct {
	mu     sync.Mutex
	dir    string
	loaded bool
	next   int
	uids   map[string]int
}

var boxScopes = &boxScopeUIDs{}

// SetBoxStateDir 设置范围 uid 登记表所在的目录（工作目录的 .diana/）。没设时登记只在
// 内存里，测试用；正式运行时调用方在第一次用 box 之前设好。
func SetBoxStateDir(dir string) {
	boxScopes.mu.Lock()
	defer boxScopes.mu.Unlock()
	if boxScopes.dir != dir {
		boxScopes.dir, boxScopes.loaded = dir, false
	}
}

type boxScopeFile struct {
	Next   int            `json:"next"`
	Scopes map[string]int `json:"scopes"`
}

func (s *boxScopeUIDs) uidFor(scope string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.next, s.uids = boxScopeUIDBase, map[string]int{}
		if s.dir != "" {
			data, err := os.ReadFile(filepath.Join(s.dir, boxScopesFile))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return 0, boxErrorf("工作区范围登记读取失败：%v", err)
			}
			if err == nil {
				var file boxScopeFile
				if err := json.Unmarshal(data, &file); err != nil {
					return 0, boxErrorf("工作区范围登记损坏：%v", err)
				}
				s.next = max(file.Next, boxScopeUIDBase)
				for name, uid := range file.Scopes {
					s.uids[name] = uid
					s.next = max(s.next, uid+1)
				}
			}
		}
		s.loaded = true
	}
	if uid, ok := s.uids[scope]; ok {
		return uid, nil
	}
	if s.next >= boxScopeUIDLimit {
		return 0, boxErrorf("工作区范围已经用完 uid 段")
	}
	uid := s.next
	s.uids[scope] = uid
	if s.dir != "" {
		// 先落盘再交出去：没记下来的 uid 重启后会被分给别的范围。
		if err := s.saveLocked(uid + 1); err != nil {
			delete(s.uids, scope)
			return 0, err
		}
	}
	s.next = uid + 1
	return uid, nil
}

func (s *boxScopeUIDs) saveLocked(next int) error {
	data, err := json.MarshalIndent(boxScopeFile{Next: next, Scopes: s.uids}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return boxErrorf("工作区范围登记写入失败：%v", err)
	}
	tmp, err := os.CreateTemp(s.dir, boxScopesFile+".*")
	if err != nil {
		return boxErrorf("工作区范围登记写入失败：%v", err)
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Rename(tmp.Name(), filepath.Join(s.dir, boxScopesFile))
	}
	if writeErr != nil {
		_ = os.Remove(tmp.Name())
		return boxErrorf("工作区范围登记写入失败：%v", writeErr)
	}
	return nil
}

// ---- 空闲回收 --------------------------------------------------------------------

// BoxIdleTimeout 是共享容器所有范围都空闲多久后停掉（删容器、留卷）。
const BoxIdleTimeout = 15 * time.Minute

// StopIdleBoxes 停掉空闲超过 idle 的共享容器，卷保留，下次使用时重建。本进程起的按
// 最近使用时间判断；上一次运行留下的不知道用过没有，按容器启动时间判断，并且容器
// 里还有命令在跑（/tmp/.run-* 在）时不动。旧版每会话一个的容器由 MigrateLegacyBoxes 处理。
func StopIdleBoxes(ctx context.Context, now time.Time, idle time.Duration) {
	docker, reason := boxRuntime()
	if reason != "" {
		return
	}
	stopIdleBoxes(ctx, docker, now, idle)
}

func stopIdleBoxes(ctx context.Context, docker dockerRunner, now time.Time, idle time.Duration) {
	res := docker(ctx, dockerCall{args: []string{"ps", "--filter", "label=" + boxLabel + "=1", "--filter", "name=^" + boxContainerName + "$", "--format", "{{.Names}}"}, timeout: boxControlTimeout})
	if res.code != 0 || strings.TrimSpace(string(res.stdout)) != boxContainerName {
		return
	}
	if host := registeredBoxContainer(); host != nil {
		host.StopIfIdle(ctx, now, idle)
		return
	}
	info := docker(ctx, dockerCall{args: []string{"inspect", "--format", "{{.State.StartedAt}}", boxContainerName}, timeout: boxControlTimeout})
	if info.code != 0 {
		return
	}
	started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(info.stdout)))
	if err != nil || now.Sub(started) < idle {
		return
	}
	if boxContainerHasRuns(ctx, docker, boxContainerName) {
		return
	}
	_ = docker(ctx, dockerCall{args: []string{"rm", "--force", boxContainerName}, timeout: boxControlTimeout})
}

func boxContainerHasRuns(ctx context.Context, docker dockerRunner, name string) bool {
	return docker(ctx, dockerCall{args: []string{"exec", name, "/bin/sh", "-c", `ls /tmp/.run-* >/dev/null 2>&1`}, timeout: boxControlTimeout}).code == 0
}

// ---- 旧版每会话容器的迁移 ----------------------------------------------------------

// boxLegacyPrefix 是旧版每会话一个的容器和卷的名字前缀（diana-box-<哈希>、diana-work-<哈希>）。
const (
	boxLegacyContainerPrefix = boxContainerName + "-"
	boxLegacyVolumePrefix    = boxVolumeName + "-"
	// boxLegacyKeyLabel 是旧版卷上记会话 key（十六进制）的标签。
	boxLegacyKeyLabel = "diana.box.key"
)

// boxLegacyCopyScript 在一次性的迁移容器里把旧卷（/old）的内容并进共享卷里对应的范围
// 目录（/new/$1），交给范围 uid（$2）。cp -n 不覆盖：迁移前范围已经在新布局里写过的
// 文件以新的为准。chown -h 只改链接本身，-R 不跟着软链接进目录，旧卷里的链接指不到
// 别的范围。迁移容器不联网，只挂这两个卷。
const boxLegacyCopyScript = `set -e
d="/new/$1"
mkdir -p -- "$d"
cp -a -n /old/. "$d"/
chown -R -h "$2:$2" -- "$d"
chmod 0700 -- "$d"`

// MigrateLegacyBoxes 把旧版（每会话一个容器、一个卷）留下的卷搬进共享卷对应的范围
// 目录，搬完删掉旧容器和旧卷。旧容器里还有命令在跑时跳过，下一轮再搬。返回值表示
// 是否还有没搬完的。
//
// 一次性迁移：2026-11 加入，保留 6 个月。
func MigrateLegacyBoxes(ctx context.Context) bool {
	docker, reason := boxRuntime()
	if reason != "" {
		return false
	}
	return migrateLegacyBoxes(ctx, docker)
}

func migrateLegacyBoxes(ctx context.Context, docker dockerRunner) bool {
	res := docker(ctx, dockerCall{args: []string{"volume", "ls", "--filter", "label=" + boxLabel + "=1", "--format", "{{.Name}}"}, timeout: boxControlTimeout})
	if res.code != 0 {
		return true
	}
	pending := false
	for _, name := range strings.Fields(string(res.stdout)) {
		if !strings.HasPrefix(name, boxLegacyVolumePrefix) {
			continue
		}
		if !migrateLegacyBox(ctx, docker, name) {
			pending = true
		}
	}
	return pending
}

func migrateLegacyBox(ctx context.Context, docker dockerRunner, volume string) bool {
	info := docker(ctx, dockerCall{args: []string{"volume", "inspect", "--format", "{{index .Labels \"" + boxLegacyKeyLabel + "\"}}|{{index .Labels \"" + boxLayoutLabel + "\"}}", volume}, timeout: boxControlTimeout})
	if info.code != 0 {
		return false
	}
	keyHex, layout, _ := strings.Cut(strings.TrimSpace(string(info.stdout)), "|")
	if layout == boxLayoutScopes {
		// 新布局的卷（比如测试起的共享卷）不是旧版留下的，不动。
		return true
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) == 0 {
		log.Printf("diana agent: 旧工作区卷 %s 没有会话标签，跳过迁移", volume)
		return true
	}
	legacyContainer := boxLegacyContainerPrefix + strings.TrimPrefix(volume, boxLegacyVolumePrefix)
	if docker(ctx, dockerCall{args: []string{"inspect", "--format", "{{.Id}}", legacyContainer}, timeout: boxControlTimeout}).code == 0 {
		if boxContainerHasRuns(ctx, docker, legacyContainer) {
			return false
		}
		if res := docker(ctx, dockerCall{args: []string{"rm", "--force", legacyContainer}, timeout: boxControlTimeout}); res.code != 0 {
			return false
		}
	}
	workBox, err := BoxFor(string(key))
	if err != nil {
		return false
	}
	// 先把共享容器和卷准备好：迁移容器要挂共享卷，范围目录也要先归到范围 uid 名下。
	if err := workBox.Ensure(ctx); err != nil {
		log.Printf("diana agent: 旧工作区卷 %s 迁移失败：%v", volume, err)
		return false
	}
	copyArgs := []string{
		"run", "--rm",
		"--network", "none",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--user", "0:0",
		"--volume", volume + ":/old:ro",
		"--volume", workBox.host.volume + ":/new",
		"--entrypoint", "/bin/sh",
		workBox.host.cfg.Image,
		"-c", boxLegacyCopyScript, "sh", workBox.scope, strconv.Itoa(workBox.uid),
	}
	if res := docker(ctx, dockerCall{args: copyArgs, timeout: boxBuildTimeout}); res.code != 0 {
		log.Printf("diana agent: 旧工作区卷 %s 迁移失败：%s", volume, strings.TrimSpace(res.stderr))
		return false
	}
	if res := docker(ctx, dockerCall{args: []string{"volume", "rm", "--force", volume}, timeout: boxControlTimeout}); res.code != 0 {
		return false
	}
	log.Printf("diana agent: 旧工作区卷 %s 已并入共享工作区", volume)
	return true
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
	// DOCKER_HOST / DOCKER_CONTEXT 决定 CLI 连哪个守护进程，rootless 部署常用它们指路。
	for _, key := range []string{"DOCKER_CONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_RUNTIME_DIR", "SYSTEMROOT", "TEMP", "TMP"} {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}
