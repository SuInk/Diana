// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker 记录每次调用，按第一个参数（及子命令）回放预设结果。
type fakeDocker struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string, stdin []byte) dockerResult
}

func (f *fakeDocker) run(_ context.Context, call dockerCall) dockerResult {
	var stdin []byte
	if call.stdin != nil {
		stdin, _ = io.ReadAll(call.stdin)
	}
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), call.args...))
	f.mu.Unlock()
	res := f.reply(call.args, stdin)
	if call.stdout != nil && len(res.stdout) > 0 {
		_, _ = call.stdout.Write(res.stdout)
	}
	return res
}

func (f *fakeDocker) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		verb := c[0]
		if (verb == "volume" || verb == "image" || verb == "network") && len(c) > 1 {
			verb += " " + c[1]
		}
		if verb == "run" && slices.Contains(c, "--rm") {
			verb = "run firewall"
		}
		out = append(out, verb)
	}
	return out
}

func testBoxConfig() BoxConfig {
	return BoxConfig{Image: BuiltinBoxImage(), Memory: "1g", CPUs: 2, PIDs: 256, Network: true}
}

// testScope 是挂在一个新共享容器上的会话范围。
func testScope(cfg BoxConfig, docker dockerRunner, key string, uid int) *WorkBox {
	return &WorkBox{host: newBoxContainer(cfg, docker), key: key, scope: boxScopeName(key), uid: uid}
}

func TestBoxRunArgsMatchYujiaxueIsolation(t *testing.T) {
	c := newBoxContainer(testBoxConfig(), nil)
	args := strings.Join(c.runArgs(), " ")
	for _, want := range []string{
		"--name diana-sandbox ", "--user 1000:1000", "--cap-drop ALL", "--cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER",
		"--security-opt no-new-privileges", "--read-only", "--network " + boxNetworkName, "--volume diana-sandbox:/work",
		"--tmpfs /tmp:rw,nosuid,nodev,size=256m", "--memory 1g", "--cpus 2", "--pids-limit 256",
		"--workdir /work", "--hostname box", "--init", "--label " + boxLayoutLabel + "=" + boxLayoutScopes, c.cfg.Image + " sleep infinity",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("run args missing %q:\n%s", want, args)
		}
	}
	// 容器里不能有机密：不挂宿主目录，容器本身不带环境变量（HOME 等按范围在 exec 时给）。
	for i, arg := range c.runArgs() {
		if arg == "--volume" && c.runArgs()[i+1] != "diana-sandbox:/work" {
			t.Fatalf("box must only mount its named volume, got %q", c.runArgs()[i+1])
		}
		if arg == "--env" {
			t.Fatalf("box container must not receive env %q", c.runArgs()[i+1])
		}
		if strings.Contains(arg, "docker.sock") {
			t.Fatal("box must never see a docker socket")
		}
	}
	offline := newBoxContainer(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, nil)
	if !strings.Contains(strings.Join(offline.runArgs(), " "), "--network none") {
		t.Fatal("network=false must use --network none")
	}
	if got := BoxConfigFromEnv().Memory; os.Getenv(boxMemoryEnv) == "" && got != "2g" {
		t.Fatalf("default memory = %q, want 2g", got)
	}
}

// 命令以范围 uid、在范围目录里跑，HOME/TMPDIR/包缓存都指向范围自己的目录。
func TestBoxScopeExecArgs(t *testing.T) {
	b := testScope(testBoxConfig(), nil, "bot\x00group:1", 10007)
	args := strings.Join(b.execArgs(false), " ")
	root := "/work/" + boxScopeName("bot\x00group:1")
	for _, want := range []string{
		"exec --user 10007:10007 --workdir " + root, "--env HOME=" + root + "/.home", "--env TMPDIR=" + root + "/tmp",
		"--env UV_CACHE_DIR=" + root + "/.home/.cache/uv", "--env NPM_CONFIG_PREFIX=" + root + "/.home/.npm-global",
		"--env PATH=" + root + "/.home/.local/bin:", " diana-sandbox",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("exec args missing %q:\n%s", want, args)
		}
	}
	if b.Root() != root || !strings.HasPrefix(b.scope, "s-") {
		t.Fatalf("root = %q", b.Root())
	}
	if other := testScope(testBoxConfig(), nil, "bot\x00group:2", 10008); other.Root() == b.Root() {
		t.Fatal("two sessions must not share a scope directory")
	}
}

func TestBoxFirewallBlocksHostAndPrivateRanges(t *testing.T) {
	previous := boxHostAddresses
	boxHostAddresses = func() ([]string, []string) { return []string{"203.0.113.7/32"}, []string{"2001:db8::7/128"} }
	t.Cleanup(func() { boxHostAddresses = previous })
	b := newBoxContainer(testBoxConfig(), nil)
	args := b.firewallArgs()
	v4, v6 := args[len(args)-2], args[len(args)-1]
	for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "203.0.113.7/32"} {
		if !strings.Contains(v4, "-A OUTPUT -d "+cidr+" -j REJECT") {
			t.Fatalf("IPv4 firewall must reject %s:\n%s", cidr, v4)
		}
	}
	for _, cidr := range []string{"fc00::/7", "fe80::/10", "::1/128", "2001:db8::7/128"} {
		if !strings.Contains(v6, "-A OUTPUT -d "+cidr+" -j REJECT") {
			t.Fatalf("IPv6 firewall must reject %s:\n%s", cidr, v6)
		}
	}
	for _, rules := range []string{v4, v6} {
		// 整张 filter 表一次提交：以 *filter 开头、COMMIT 结尾，回环先放行，不靠先清空再追加。
		if !strings.HasPrefix(rules, "*filter\n") || !strings.HasSuffix(rules, "COMMIT\n") || !strings.Contains(rules, "-A OUTPUT -o lo -j ACCEPT") {
			t.Fatalf("rules must be one atomic iptables-restore table:\n%s", rules)
		}
	}
	if !strings.Contains(boxFirewallScript, "iptables-restore") || !strings.Contains(boxFirewallScript, "ip6tables-restore") || strings.Contains(boxFirewallScript, " -F ") {
		t.Fatalf("firewall must be applied with iptables-restore, not flush-and-append:\n%s", boxFirewallScript)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--rm", "--network container:" + b.container, "--cap-drop ALL", "--cap-add NET_ADMIN"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("firewall args missing %q: %s", want, joined)
		}
	}
}

// testBoxShape 是按 testBoxConfig 起出来的容器在 inspect 里的网络模式、资源上限和额外能力。
const testBoxShape = "diana-sandbox 1073741824 2000000000 256 " + boxCapAdd

// running 的容器在 fake inspect 里的样子：运行中、镜像、ID、启动时间，再加 testBoxShape。
func boxInspectReply(image, id, started string) dockerResult {
	return boxInspectReplyShape(image, id, started, "diana-sandbox 1073741824 2000000000 256 "+`["CAP_FOWNER","CAP_CHOWN","CAP_DAC_OVERRIDE"]`)
}

func boxInspectReplyShape(image, id, started, shape string) dockerResult {
	return dockerResult{stdout: []byte("true " + image + " " + id + " " + started + " " + shape + "\n")}
}

func TestBoxEnsureLifecycle(t *testing.T) {
	running := false
	started := "2026-11-15T01:00:00Z"
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			if running {
				return boxInspectReply(BuiltinBoxImage(), "c1", started)
			}
			return dockerResult{code: 1, stderr: "No such object"}
		case "image":
			return dockerResult{code: 1}
		case "network":
			if args[1] == "inspect" {
				return dockerResult{code: 1}
			}
		case "run":
			if !slices.Contains(args, "--rm") {
				running = true
			}
		}
		return dockerResult{}
	}}
	b := testScope(testBoxConfig(), fake.run, "k", 10001)
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 防火墙在任何 exec 之前写：容器里还没跑过任何东西时出站就已经收紧。之后 root 把
	// /work 收成 0711，再建这个范围的目录、交给范围 uid，最后范围 uid 自己建子目录。
	want := []string{"inspect", "image inspect", "image inspect", "build", "network inspect", "network create", "volume create", "run", "inspect", "run firewall", "exec", "exec", "exec"}
	if got := fake.verbs(); !slices.Equal(got, want) {
		t.Fatalf("first ensure:\n got %v\nwant %v", got, want)
	}
	calls := fake.calls
	if init := strings.Join(calls[10], " "); !strings.Contains(init, "--user 0:0") || !strings.Contains(init, boxRootInitScript) {
		t.Fatalf("root init: %s", init)
	}
	if dir := strings.Join(calls[11], " "); !strings.Contains(dir, "--user 0:0") || !strings.HasSuffix(dir, b.scope+" 10001") {
		t.Fatalf("scope dir: %s", dir)
	}
	if skeleton := strings.Join(calls[12], " "); !strings.Contains(skeleton, "--user 10001:10001") || !strings.HasSuffix(skeleton, b.Root()) {
		t.Fatalf("scope skeleton must run as the scope uid: %s", skeleton)
	}
	// 同一进程里第二次使用：容器还是写防火墙时那个，范围也准备过，只 inspect。
	fake.calls = nil
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect"}) {
		t.Fatalf("warm ensure: %v", got)
	}
	// 同一个容器里的另一个范围：不重建容器，只准备自己的目录。
	other := &WorkBox{host: b.host, key: "k2", scope: boxScopeName("k2"), uid: 10002}
	fake.calls = nil
	if err := other.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "exec", "exec"}) {
		t.Fatalf("second scope ensure: %v", got)
	}
	// 容器被外部重启（ID 不变、启动时间变了）：网络命名空间是新的，规则要重写；范围目录重新核对。
	started = "2026-11-15T02:00:00Z"
	fake.calls = nil
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "run firewall", "exec", "exec"}) {
		t.Fatalf("restarted container must get its firewall again: %v", got)
	}
	// 新进程接手旧容器：不知道防火墙写没写，重写一遍。
	fresh := testScope(testBoxConfig(), fake.run, "k", 10001)
	fake.calls = nil
	if err := fresh.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "run firewall", "exec", "exec"}) {
		t.Fatalf("adopted ensure: %v", got)
	}
}

func TestBoxEnsureRemovesContainerWhenFirewallFails(t *testing.T) {
	running := false
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch {
		case args[0] == "inspect":
			if running {
				return boxInspectReply(BuiltinBoxImage(), "c1", "2026-11-15T01:00:00Z")
			}
			return dockerResult{code: 1}
		case args[0] == "run" && slices.Contains(args, "--rm"):
			return dockerResult{code: 1, stderr: "iptables: permission denied"}
		case args[0] == "run":
			running = true
		}
		return dockerResult{}
	}}
	b := testScope(testBoxConfig(), fake.run, "k", 10001)
	if err := b.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "防火墙") {
		t.Fatalf("want firewall error, got %v", err)
	}
	got := fake.verbs()
	if got[len(got)-1] != "rm" || slices.Contains(got, "exec") {
		t.Fatalf("networked box without firewall must be removed before anything runs in it: %v", got)
	}
}

func TestBoxEnsureRecreatesOnImageChange(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "inspect" {
			return boxInspectReply("diana-box:old", "c1", "2026-11-15T01:00:00Z")
		}
		return dockerResult{}
	}}
	b := testScope(testBoxConfig(), fake.run, "k", 10001)
	_ = b.Ensure(context.Background())
	if got := fake.verbs(); len(got) < 2 || got[1] != "rm" {
		t.Fatalf("stale image must be replaced: %v", got)
	}
}

func fakeTestBox(fake *fakeDocker) *WorkBox {
	return testScope(BoxConfig{Image: "x", Memory: "1g", CPUs: 2, PIDs: 256, Network: true}, fake.run, "k", 10001)
}

// 能力的写法因 Docker 版本而异（CAP_CHOWN 或 CHOWN，顺序不定），都要认成同一个，
// 否则每次使用都会重建共享容器。
func TestNormalizeBoxCaps(t *testing.T) {
	for _, raw := range []string{`["CHOWN","DAC_OVERRIDE","FOWNER"]`, `["CAP_CHOWN","CAP_DAC_OVERRIDE","CAP_FOWNER"]`, `["cap_fowner","CAP_CHOWN","dac_override"]`} {
		if got := normalizeBoxCaps(raw); got != boxCapAdd {
			t.Fatalf("normalize(%s) = %q", raw, got)
		}
	}
	if got := normalizeBoxCaps("null"); got == boxCapAdd {
		t.Fatal("no caps must not match")
	}
}

// 部署方改了网络开关或资源上限：旧容器的网络模式、内存、CPU、进程数对不上，删掉重建。
// 能力集合对不上（比如还是旧版不带 CHOWN 的容器）也重建。
func TestBoxEnsureRecreatesOnShapeChange(t *testing.T) {
	for name, shape := range map[string]string{
		"offline container, networked config": "none 1073741824 2000000000 256 " + `["CHOWN","DAC_OVERRIDE","FOWNER"]`,
		"memory changed":                      "diana-sandbox 536870912 2000000000 256 " + `["CHOWN","DAC_OVERRIDE","FOWNER"]`,
		"cpus changed":                        "diana-sandbox 1073741824 1000000000 256 " + `["CHOWN","DAC_OVERRIDE","FOWNER"]`,
		"pids changed":                        "diana-sandbox 1073741824 2000000000 64 " + `["CHOWN","DAC_OVERRIDE","FOWNER"]`,
		"caps reduced":                        "diana-sandbox 1073741824 2000000000 256 " + `["CAP_CHOWN"]`,
		"caps missing":                        "diana-sandbox 1073741824 2000000000 256 null",
	} {
		fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
			if args[0] == "inspect" {
				return boxInspectReplyShape(BuiltinBoxImage(), "c1", "s", shape)
			}
			return dockerResult{}
		}}
		b := testScope(testBoxConfig(), fake.run, "k", 10001)
		_ = b.Ensure(context.Background())
		if got := fake.verbs(); len(got) < 2 || got[1] != "rm" {
			t.Fatalf("%s: container must be recreated: %v", name, got)
		}
	}
	if c := newBoxContainer(testBoxConfig(), nil); c.wantShape() != testBoxShape {
		t.Fatalf("wantShape = %q", c.wantShape())
	}
	for in, want := range map[string]int64{"1g": 1 << 30, "512m": 512 << 20, "1.5g": 3 << 29, "2048k": 2 << 20, "100b": 100, "4096": 4096} {
		if got, ok := parseBoxMemory(in); !ok || got != want {
			t.Fatalf("parseBoxMemory(%q) = %d %v", in, got, ok)
		}
	}
}

// 宿主写不了 IPv6 规则（防火墙脚本退出 7）：关掉容器里的 IPv6 重建，之后只靠 IPv4 规则。
func TestBoxFallsBackToIPv4OnlyWithoutIPv6Tables(t *testing.T) {
	t.Cleanup(func() { boxIPv6Off.Store(false) })
	running, sysctlRuns := false, 0
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch {
		case args[0] == "inspect":
			if running {
				return boxInspectReply(BuiltinBoxImage(), "c1", "s")
			}
			return dockerResult{code: 1}
		case args[0] == "rm":
			running = false
		case args[0] == "run" && slices.Contains(args, "--rm"):
			if !boxIPv6Off.Load() {
				return dockerResult{code: boxFirewallNoIPv6Tables, stderr: "ip6tables-restore: unable to initialize table 'filter'"}
			}
		case args[0] == "run":
			running = true
			if slices.Contains(args, "net.ipv6.conf.all.disable_ipv6=1") {
				sysctlRuns++
			}
		}
		return dockerResult{}
	}}
	b := testScope(testBoxConfig(), fake.run, "k", 10001)
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !boxIPv6Off.Load() || sysctlRuns != 1 || b.host.firewalledFor == "" {
		t.Fatalf("ipv6Off=%v sysctlRuns=%d firewalled=%q verbs=%v", boxIPv6Off.Load(), sysctlRuns, b.host.firewalledFor, fake.verbs())
	}
}

// 重置只清这个范围：以范围 uid 停掉它的进程、删光它的目录，不动共享容器和卷，也不碰别的范围。
func TestBoxResetClearsOnlyTheScope(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "inspect" {
			return boxInspectReply("x", "c1", "s")
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	if err := b.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "exec", "exec", "exec"}) {
		t.Fatalf("reset: %v", got)
	}
	for _, call := range fake.calls[1:] {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "--user 10001:10001") {
			t.Fatalf("reset must run as the scope uid: %s", joined)
		}
	}
	if kill := fake.calls[2]; !slices.Equal(kill[len(kill)-4:], []string{"pkill", "-KILL", "-U", "10001"}) {
		t.Fatalf("kill: %v", kill)
	}
	if clear := fake.calls[3]; clear[len(clear)-1] != b.Root() || !slices.Contains(clear, boxScopeClearScript) {
		t.Fatalf("clear: %v", clear)
	}
	// 清空后下次使用重建目录骨架。
	fake.calls = nil
	_ = b.Ensure(context.Background())
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "exec", "exec"}) {
		t.Fatalf("ensure after reset: %v", got)
	}
}

// 124 才是 timeout 到点；137 可能是命令自己被 OOM 杀掉，不能报成超时。
func TestBoxExecTimeoutCapAndExitCodes(t *testing.T) {
	code := 124
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxExecScript) {
				n := len(args)
				if args[n-7] == "sleep 9999" && args[n-8] != "600" {
					t.Errorf("timeout must be capped at 600s, got %s", args[n-8])
				}
				if out := args[n-6]; !strings.HasPrefix(out, args[n-2]+"/"+boxOutputDir+"/") || !strings.HasSuffix(out, ".log") {
					t.Errorf("full output must stay in the scope directory, got %s", out)
				}
				if args[n-1] != "128" {
					t.Errorf("scope process limit = %s, want 128", args[n-1])
				}
				return dockerResult{code: code}
			}
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	if res, err := b.Exec(context.Background(), "sleep 9999", time.Hour); err != nil || !res.TimedOut {
		t.Fatalf("124: res=%+v err=%v", res, err)
	}
	code = 137
	if res, err := b.Exec(context.Background(), "oom", 0); err != nil || res.TimedOut || res.ExitCode != 137 {
		t.Fatalf("137: res=%+v err=%v", res, err)
	}
}

func TestBoxExecRecreatesVanishedContainerOnce(t *testing.T) {
	attempts := 0
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxExecScript) {
				attempts++
				if attempts == 1 {
					return dockerResult{code: 1, stderr: "Error response from daemon: No such container: diana-sandbox"}
				}
			}
		}
		return dockerResult{}
	}}
	if res, err := fakeTestBox(fake).Exec(context.Background(), "true", 0); err != nil || res.ExitCode != 0 || attempts != 2 {
		t.Fatalf("res=%+v err=%v attempts=%d", res, err, attempts)
	}
}

// 输出读回只要尾部窗口：头一行是总字节和总行数，后面是窗口内容，起点按两者推出来。
func TestBoxOutputTail(t *testing.T) {
	var gotPath string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "exec" && slices.Contains(args, boxOutputTailScript) {
			if args[len(args)-1] != "8" {
				t.Errorf("window = %s", args[len(args)-1])
			}
			gotPath = args[len(args)-2]
			return dockerResult{stdout: []byte("100 12\nline-11\n")}
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	tail, start, size, lines, err := b.OutputTail(context.Background(), ".output/a.log", 8)
	if err != nil || string(tail) != "line-11\n" || start != 92 || size != 100 || lines != 12 {
		t.Fatalf("tail=%q start=%d size=%d lines=%d err=%v", tail, start, size, lines, err)
	}
	if gotPath != b.Root()+"/.output/a.log" {
		t.Fatalf("path = %q", gotPath)
	}
}

func withFakeBoxRuntime(t *testing.T) {
	t.Helper()
	previous := boxProbe
	boxProbe = func(BoxConfig) (dockerRunner, string) {
		return func(context.Context, dockerCall) dockerResult { return dockerResult{} }, ""
	}
	resetAvailability := func() {
		boxAvailability.mu.Lock()
		boxAvailability.checked = time.Time{}
		boxAvailability.mu.Unlock()
	}
	resetAvailability()
	t.Cleanup(func() {
		boxProbe = previous
		resetAvailability()
	})
}

// 同一个 key 并发取 box 只能得到同一个实例；不同 key 拿到不同的范围、不同的 uid，但共用
// 一个容器。
func TestBoxForIsAtomic(t *testing.T) {
	withFakeBoxRuntime(t)
	t.Cleanup(func() {
		boxRegistry.Delete("concurrent")
		boxRegistry.Delete("concurrent-2")
	})
	var wg sync.WaitGroup
	got := make([]*WorkBox, 64)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := BoxFor("concurrent")
			if err != nil {
				t.Error(err)
			}
			got[i] = b
		}()
	}
	wg.Wait()
	for _, b := range got {
		if b != got[0] {
			t.Fatal("concurrent BoxFor returned different boxes for one key")
		}
	}
	other, err := BoxFor("concurrent-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.host != got[0].host || other.scope == got[0].scope || other.uid == got[0].uid {
		t.Fatalf("scopes must share the container but not dir/uid: %+v vs %+v", other, got[0])
	}
}

// 范围 uid 落盘、重启后照旧，新范围接着往后分，永不复用；登记表坏了就拒绝，不瞎分。
func TestBoxScopeUIDsPersist(t *testing.T) {
	dir := t.TempDir()
	first := &boxScopeUIDs{dir: dir}
	a, err := first.uidFor("s-a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := first.uidFor("s-b")
	if again, _ := first.uidFor("s-a"); again != a || a != boxScopeUIDBase || b != boxScopeUIDBase+1 {
		t.Fatalf("a=%d b=%d again=%d", a, b, again)
	}
	reloaded := &boxScopeUIDs{dir: dir}
	if got, _ := reloaded.uidFor("s-b"); got != b {
		t.Fatalf("after restart s-b = %d, want %d", got, b)
	}
	if got, _ := reloaded.uidFor("s-c"); got != boxScopeUIDBase+2 {
		t.Fatalf("new scope after restart = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, boxScopesFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&boxScopeUIDs{dir: dir}).uidFor("s-d"); err == nil {
		t.Fatal("corrupt registry must refuse to hand out uids")
	}
}

func TestBoxResolvePath(t *testing.T) {
	b := testScope(testBoxConfig(), nil, "k", 10001)
	root := b.Root()
	for in, want := range map[string]string{
		"a.txt":                 root + "/a.txt",
		"inbox/1.png":           root + "/inbox/1.png",
		root + "/x/./y":         root + "/x/y",
		"/work/out.gif":         root + "/out.gif",
		"/work":                 root,
		"":                      root,
		"/work/inbox/video.mp4": root + "/inbox/video.mp4",
	} {
		if got, err := b.ResolvePath(in); err != nil || got != want {
			t.Fatalf("%q → %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "/work/../etc", root + "/../s-other/x", "/workx/a", "a\nb"} {
		if _, err := b.ResolvePath(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestBoxCopyOutEnforcesLimit(t *testing.T) {
	var fake *fakeDocker
	b := (*WorkBox)(nil)
	fake = &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxCopyOutScript) {
				if args[len(args)-1] != b.Root() || !strings.Contains(strings.Join(args, " "), "--user 10001:10001") {
					t.Errorf("copy out must check against the scope root as the scope uid: %v", args)
				}
				return dockerResult{stdout: []byte(b.Root() + "/out.png\nPNGDATA")}
			}
		}
		return dockerResult{}
	}}
	b = fakeTestBox(fake)
	real, data, err := b.CopyOut(context.Background(), "/work/out.png", 1024)
	if err != nil || real != b.Root()+"/out.png" || string(data) != "PNGDATA" {
		t.Fatalf("real=%q data=%q err=%v", real, data, err)
	}
	if _, _, err := b.CopyOut(context.Background(), "out.png", 3); err == nil {
		t.Fatal("oversized output must be rejected")
	}
}

func TestBoxDaemonPolicy(t *testing.T) {
	cases := []struct {
		goos, host, security, osName string
		ok                           bool
	}{
		{"linux", "unix:///run/user/1000/docker.sock", `["name=seccomp","name=rootless"]`, "Ubuntu 24.04", true},
		{"linux", "unix:///var/run/docker.sock", `["name=seccomp"]`, "Ubuntu 24.04", false},
		{"android", "unix:///data/docker.sock", `["name=seccomp"]`, "Debian", false},
		{"linux", "unix:///run/desktop/docker.sock", `["name=seccomp"]`, "Docker Desktop", true},
		{"darwin", "unix:///var/run/docker-desktop.sock", `["name=seccomp","name=cgroupns"]`, "Docker Desktop", true},
		{"darwin", "unix:///var/run/colima.sock", `["name=seccomp"]`, "Ubuntu 24.04 LTS", true},
		{"windows", "npipe:////./pipe/docker_engine", `["name=seccomp"]`, "Docker Desktop", true},
		// 远端守护进程一律不用，哪怕是 macOS 上连过去、哪怕自称 rootless。
		{"darwin", "tcp://10.0.0.5:2376", `["name=seccomp"]`, "Ubuntu 24.04", false},
		{"darwin", "ssh://build@host", `["name=rootless"]`, "Ubuntu 24.04", false},
		{"linux", "", `["name=rootless"]`, "Ubuntu 24.04", false},
	}
	for _, c := range cases {
		if got := boxDaemonPolicy(c.goos, c.host, c.security, c.osName) == ""; got != c.ok {
			t.Fatalf("%+v: ok=%v", c, got)
		}
	}
}

// TestBoxRealDocker 用真 Docker 跑一遍：联网、挡宿主回环和私网、超时杀进程组、文件进出、
// 范围之间互相看不到、重置只清自己。容器和卷用测试专用的名字，不碰正在用的 diana-sandbox。
// 需要本机有 Docker：DIANA_BOX_DOCKER_TEST=1 scripts/test-serial.sh ./model/agent
func TestBoxRealDocker(t *testing.T) {
	if os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		t.Skip("set DIANA_BOX_DOCKER_TEST=1 to run against a real Docker daemon")
	}
	docker, reason := probeBoxRuntime(BoxConfig{})
	if reason != "" {
		t.Fatal(reason)
	}
	host := newBoxContainer(testBoxConfig(), docker)
	host.container, host.volume = "diana-test-box", "diana-test-work"
	b := &WorkBox{host: host, key: "test-a", scope: boxScopeName("test-a"), uid: 10901}
	other := &WorkBox{host: host, key: "test-b", scope: boxScopeName("test-b"), uid: 10902}
	ctx := context.Background()
	t.Cleanup(func() {
		_ = docker(context.Background(), dockerCall{args: []string{"rm", "--force", host.container}, timeout: time.Minute})
		_ = docker(context.Background(), dockerCall{args: []string{"volume", "rm", "--force", host.volume}, timeout: time.Minute})
	})
	runIn := func(w *WorkBox, command string, timeout time.Duration) (BoxExecResult, string) {
		t.Helper()
		res, err := w.Exec(ctx, command, timeout)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		tail, _, _, _, err := w.OutputTail(ctx, res.OutputPath, 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		return res, string(tail)
	}
	run := func(command string, timeout time.Duration) (BoxExecResult, string) {
		t.Helper()
		return runIn(b, command, timeout)
	}
	root := b.Root()
	// 普通命令要马上返回：后台的磁盘检查不能拖住 docker exec。
	quick := time.Now()
	run(`true`, 0)
	if elapsed := time.Since(quick); elapsed > 10*time.Second {
		t.Fatalf("a trivial command took %v", elapsed)
	}
	firstIdentity := host.identity
	if res, out := run(`id -u; pwd; echo "$HOME"; touch /etc/x 2>&1 | head -1`, 0); res.ExitCode != 0 || out != "10901\n"+root+"\n"+root+"/.home\ntouch: cannot touch '/etc/x': Read-only file system\n" {
		t.Fatalf("identity/cwd/readonly: %+v %q", res, out)
	}
	// 范围之间：列不出 /work，读不到、写不进别人的目录，也不能在 /work 下直接建文件；
	// 能力都到不了命令手里。
	runIn(other, `echo secret > mine.txt`, 0)
	if _, out := run(`ls /work 2>&1 | head -1; cat `+other.Root()+`/mine.txt 2>&1 | head -1; touch /work/x 2>&1 | head -1; grep CapEff /proc/self/status`, 0); !strings.Contains(out, "Permission denied") || strings.Contains(out, "secret") || !strings.Contains(out, "CapEff:\t0000000000000000") {
		t.Fatalf("scopes must not see each other: %q", out)
	}
	if _, out := run(`curl -s -m 15 -o /dev/null -w '%{http_code}' https://example.com`, 0); out != "200" {
		t.Fatalf("public network must work, got %q", out)
	}
	for _, target := range []string{"http://host.docker.internal:18080/", "http://172.17.0.1/", "http://192.168.65.254/", "http://100.100.100.100/", "http://10.0.0.1/"} {
		if _, out := run(`curl -s -m 5 -o /dev/null -w '%{http_code}' `+target+`; echo " $?"`, 0); !strings.HasPrefix(out, "000 ") {
			t.Fatalf("%s must be blocked, got %q", target, out)
		}
	}
	if _, out := run(`iptables -F OUTPUT 2>&1; echo rc=$?`, 0); !strings.Contains(out, "rc=") || strings.Contains(out, "rc=0") {
		t.Fatalf("box must not be able to flush its firewall: %q", out)
	}
	start := time.Now()
	if res, _ := run(`(sleep 300 &) ; sleep 300`, 2*time.Second); !res.TimedOut || time.Since(start) > 20*time.Second {
		t.Fatalf("timeout: %+v after %v", res, time.Since(start))
	}
	if _, out := run(`ps -eo stat=,args= | grep -v '^Z' | grep -c '[s]leep 300' || true`, 0); strings.TrimSpace(out) != "0" {
		t.Fatalf("timed-out process group must be killed, sleep count %q", out)
	}
	// 调用方取消（比如这一轮被打断）：docker exec 客户端被杀不会结束容器里的进程，
	// Diana 要按 pid 杀掉整组。
	cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if _, err := b.Exec(cancelCtx, `(sleep 301 &) ; sleep 301`, 60*time.Second); err == nil {
		t.Fatal("cancelled command must return the context error")
	}
	cancel()
	if _, out := run(`ps -eo stat=,args= | grep -v '^Z' | grep -c '[s]leep 301' || true`, 0); strings.TrimSpace(out) != "0" {
		t.Fatalf("cancelled process group must be killed, sleep count %q", out)
	}
	if err := NewBoxFileStore(b).WriteFile(ctx, "inbox/hello.txt", []byte("hi"), false); err != nil {
		t.Fatal(err)
	}
	if real, data, err := b.CopyOut(ctx, "/work/inbox/hello.txt", 1024); err != nil || real != root+"/inbox/hello.txt" || string(data) != "hi" {
		t.Fatalf("copy out: %q %q %v", real, data, err)
	}
	run(`ln -sf /etc/passwd leak; ln -sf `+other.Root()+`/mine.txt peek`, 0)
	for _, link := range []string{"leak", "peek"} {
		if _, _, err := b.CopyOut(ctx, link, 1<<20); err == nil {
			t.Fatalf("symlink %s escaping the scope must be rejected", link)
		}
	}
	// 输出放不下时给尾部整行，完整输出落进范围目录，模型在容器里按行读。
	body, err := RunBoxCommand(ctx, b, `seq 1 300000`, 0)
	if err != nil || !strings.Contains(body, `"truncated": true`) || !strings.Contains(body, "300000") || !strings.Contains(body, root+"/.output/") {
		t.Fatalf("paged output: err=%v %.400s", err, body)
	}
	var paged struct {
		FullOutputPath string `json:"full_output_path"`
	}
	_ = json.Unmarshal([]byte(body), &paged)
	if _, out := run(`wc -l < `+paged.FullOutputPath, 0); strings.TrimSpace(out) != "300000" {
		t.Fatalf("full output lines in %q = %q", paged.FullOutputPath, out)
	}
	// 完整输出只留最新几份；还在跑的命令（/tmp/.run-<runID> 在）的那份不删，哪怕它最旧。
	run(`touch /tmp/.run-stillrunning && touch -d 2000-01-01 .output/20000101-000000-stillrunning.log`, 0)
	for range boxOutputKeep + 5 {
		run(`true`, 0)
	}
	if _, out := run(`ls .output | wc -l; ls .output/20000101-000000-stillrunning.log`, 0); out != strconv.Itoa(boxOutputKeep+1)+"\n.output/20000101-000000-stillrunning.log\n" {
		t.Fatalf("output rotation: %q", out)
	}
	run(`rm -f /tmp/.run-stillrunning`, 0)
	// 几十条命令、两个范围跑下来，共享容器还是最开始那个：每次使用都不该重建它。
	if host.identity != firstIdentity || firstIdentity == "" {
		t.Fatalf("shared container was recreated between commands: %q → %q", firstIdentity, host.identity)
	}
	// 容器被外部重启后网络命名空间是新的：下次使用前必须重写防火墙。
	if res := docker(ctx, dockerCall{args: []string{"restart", "--time", "1", host.container}, timeout: time.Minute}); res.code != 0 {
		t.Fatalf("restart: %s", res.stderr)
	}
	if _, out := run(`curl -s -m 5 -o /dev/null -w '%{http_code}' http://host.docker.internal:18080/; echo " $?"`, 0); !strings.HasPrefix(out, "000 ") {
		t.Fatalf("firewall lost after external restart: %q", out)
	}
	if _, err := RunBoxCommand(ctx, b, `echo boom >&2; exit 3`, 0); err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), `"exit_code": 3`) {
		t.Fatalf("failing command: %v", err)
	}
	// 磁盘上限按范围目录算：单个文件被 ulimit -f 截住；几个文件加起来超限时运行中被停掉；
	// 之后的命令和附件写入都被拒绝，删掉文件就恢复。
	b.SetDiskLimit(20 << 20)
	if _, err := b.Exec(ctx, `head -c 30M /dev/zero > big`, 0); err != nil {
		t.Fatal(err)
	}
	b.SetDiskLimit(0)
	if _, out := run(`stat -c %s big; rm -f big`, 0); func() bool { n, err := strconv.Atoi(strings.TrimSpace(out)); return err != nil || n > 20<<20 }() {
		t.Fatalf("ulimit -f must cap a single file at the limit, size %q", out)
	}
	b.SetDiskLimit(20 << 20)
	stopped, err := b.Exec(ctx, `for i in 1 2 3; do head -c 9M /dev/zero > f$i; done; sleep 30`, 60*time.Second)
	if err != nil || stopped.DiskFull == nil || stopped.DiskFull.Refused || stopped.Duration > 20*time.Second {
		t.Fatalf("over-limit command must be stopped mid-run: %+v %v", stopped, err)
	}
	refused, err := b.Exec(ctx, `echo should-not-run > ran`, 0)
	if err != nil || refused.DiskFull == nil || !refused.DiskFull.Refused {
		t.Fatalf("command over limit must be refused: %+v %v", refused, err)
	}
	if err := NewBoxFileStore(b).WriteFile(ctx, "inbox/more.txt", []byte("x"), false); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("write over limit: %v", err)
	}
	// 另一个范围不受这个范围的上限影响。
	if res, _ := runIn(other, `head -c 1M /dev/zero > ok`, 0); res.DiskFull != nil {
		t.Fatalf("other scope hit this scope's limit: %+v", res)
	}
	b.SetDiskLimit(0)
	if _, out := run(`ls ran 2>&1; rm -f f1 f2 f3`, 0); !strings.Contains(out, "No such file") {
		t.Fatalf("refused command ran: %q", out)
	}
	// 重置只清自己：自己的进程停掉、文件删光，别的范围原样。
	if _, err := b.Exec(ctx, `(sleep 302 &) ; true`, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, out := run(`ls -A tmp; ls | grep -v "^tmp$" || true; ps -eo args= | grep -c '[s]leep 302' || true`, 0); strings.TrimSpace(out) != "0" {
		t.Fatalf("reset must wipe the scope and its processes, got %q", out)
	}
	if _, out := runIn(other, `cat mine.txt`, 0); out != "secret\n" {
		t.Fatalf("reset touched another scope: %q", out)
	}
}

// 空闲回收：本进程起的共享容器按所有范围的最近使用时间，忙着的不动；上一次运行留下的
// 按启动时间，里面还有命令在跑时不动。回收只删容器，不删卷。
func TestStopIdleBoxes(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		registered     bool
		lastUsed       time.Duration
		busy           bool
		started        time.Duration
		hasRuns        bool
		wantRemoved    bool
		wantNotRunning bool
	}{
		"registered idle":        {registered: true, lastUsed: 20 * time.Minute, wantRemoved: true},
		"registered fresh":       {registered: true, lastUsed: time.Minute},
		"registered busy":        {registered: true, lastUsed: time.Hour, busy: true},
		"orphan old":             {started: time.Hour, wantRemoved: true},
		"orphan with a command":  {started: time.Hour, hasRuns: true},
		"orphan recently booted": {started: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			var removed []string
			fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
				switch args[0] {
				case "ps":
					return dockerResult{stdout: []byte(boxContainerName + "\n")}
				case "inspect":
					return dockerResult{stdout: []byte(now.Add(-tc.started).Format(time.RFC3339Nano))}
				case "exec":
					if tc.hasRuns {
						return dockerResult{}
					}
					return dockerResult{code: 2}
				case "rm":
					removed = append(removed, args[len(args)-1])
				case "volume":
					t.Errorf("idle stop must keep volumes: %v", args)
				}
				return dockerResult{}
			}}
			sharedBox.mu.Lock()
			previous := sharedBox.c
			sharedBox.c = nil
			var host *boxContainer
			if tc.registered {
				host = newBoxContainer(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, fake.run)
				host.lastUsed.Store(now.Add(-tc.lastUsed).UnixNano())
				host.firewalledFor = "c1|s"
				if tc.busy {
					host.running.Add(1)
				}
				sharedBox.c = host
			}
			sharedBox.mu.Unlock()
			t.Cleanup(func() {
				sharedBox.mu.Lock()
				sharedBox.c = previous
				sharedBox.mu.Unlock()
			})
			stopIdleBoxes(context.Background(), fake.run, now, BoxIdleTimeout)
			if got := len(removed) == 1 && removed[0] == boxContainerName; got != tc.wantRemoved {
				t.Fatalf("removed %v, want removed=%v", removed, tc.wantRemoved)
			}
			if host != nil && tc.wantRemoved && host.firewalledFor != "" {
				t.Fatal("stopped box must re-apply its firewall next time")
			}
		})
	}
}

// 回收方在锁外粗筛之后、动手之前，可能有命令刚开始：StopIfIdle 在锁内再看一遍，
// 任何范围忙着或刚用过就不动。
func TestBoxIdleActionsRecheckUnderLock(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var calls []string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		calls = append(calls, args[0])
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	other := &WorkBox{host: b.host, key: "k2", scope: boxScopeName("k2"), uid: 10002}
	b.host.lastUsed.Store(now.Add(-time.Hour).UnixNano())
	done := other.use() // 粗筛之后另一个范围有命令开始了
	if b.host.StopIfIdle(context.Background(), now, BoxIdleTimeout) {
		t.Fatal("stopped the shared box while another scope runs a command")
	}
	done()
	if b.host.StopIfIdle(context.Background(), time.Now(), BoxIdleTimeout) {
		t.Fatal("stopped a box that was just used")
	}
	if len(calls) != 0 {
		t.Fatalf("busy box got docker calls: %v", calls)
	}
	if !b.host.StopIfIdle(context.Background(), time.Now().Add(BoxIdleTimeout+time.Minute), BoxIdleTimeout) {
		t.Fatal("idle box was not stopped")
	}
}

// 范围目录超过磁盘上限：开始前就超的不执行（75 + 标记），运行中超的被停掉（只有标记）；
// 结果里带上用量和上限。上限按 KiB 传给容器里的脚本，du 按范围目录算。
func TestBoxExecDiskLimit(t *testing.T) {
	code, marker := boxDiskRefusedExit, "6291456"
	var b *WorkBox
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxExecScript) {
				n := len(args)
				if args[n-4] != "5120" || args[n-2] != b.Root() {
					t.Errorf("limit KiB = %s root = %s, want 5120 %s", args[n-4], args[n-2], b.Root())
				}
				return dockerResult{code: code}
			}
			if slices.Contains(args, boxExecCleanupScript) {
				return dockerResult{stdout: []byte(marker + "\n")}
			}
		}
		return dockerResult{}
	}}
	b = fakeTestBox(fake)
	b.SetDiskLimit(5 << 20)
	body, err := RunBoxCommand(context.Background(), b, "git clone huge", 0)
	if err == nil || body != "" || !strings.Contains(err.Error(), `"used": "6.0 GB"`) || !strings.Contains(err.Error(), `"limit": "5 MB"`) || !strings.Contains(err.Error(), "box_reset") {
		t.Fatalf("refused: %v", err)
	}
	code = 137
	res, err := b.Exec(context.Background(), "dd", 0)
	if err != nil || res.DiskFull == nil || res.DiskFull.Refused || res.DiskFull.UsedBytes != 6<<30 || res.TimedOut {
		t.Fatalf("stopped mid-run: %+v %v", res.DiskFull, err)
	}
	marker = ""
	if res, _ := b.Exec(context.Background(), "true", 0); res.DiskFull != nil {
		t.Fatal("no marker must mean no disk limit hit")
	}
	if b.SetDiskLimit(0); b.diskLimitBytes() != DefaultBoxDiskLimitBytes {
		t.Fatal("zero limit must fall back to the default")
	}
}

func TestBoxWriteFileDiskLimit(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxStoreWriteScript) {
				return dockerResult{code: boxDiskRefusedExit, stdout: []byte("10240\n")}
			}
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	b.SetDiskLimit(10 << 20)
	if err := NewBoxFileStore(b).WriteFile(context.Background(), "inbox/a.bin", []byte("x"), false); err == nil || !strings.Contains(err.Error(), "已用 10 MB") || !strings.Contains(err.Error(), "上限 10 MB") {
		t.Fatalf("write over limit: %v", err)
	}
}

// RunBoxCommand 的结果带上 cwd：一个容器里各会话目录不同，模型要知道自己在哪。
func TestRunBoxCommandReportsCwd(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxOutputTailScript) {
				return dockerResult{stdout: []byte("3 1\nok\n")}
			}
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	body, err := RunBoxCommand(context.Background(), b, "echo ok", 0)
	if err != nil || !strings.Contains(body, `"cwd": "`+b.Root()+`"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

// 旧版每会话一个的卷：先删旧容器，再在一次性容器里把旧卷并进共享卷对应的范围目录、交给
// 范围 uid，最后删旧卷。旧容器里还有命令在跑时这一轮不动。
func TestMigrateLegacyBoxes(t *testing.T) {
	withFakeBoxRuntime(t)
	key := "bot\x00group:legacy"
	t.Cleanup(func() { boxRegistry.Delete(key) })
	legacySuffix := boxNameFor(key)
	legacyVolume, legacyContainer := boxLegacyVolumePrefix+legacySuffix, boxLegacyContainerPrefix+legacySuffix
	hasRuns := true
	var steps []string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "volume" && args[1] == "ls":
			return dockerResult{stdout: []byte(boxVolumeName + "\n" + legacyVolume + "\n")}
		case args[0] == "volume" && args[1] == "inspect":
			return dockerResult{stdout: []byte(hex.EncodeToString([]byte(key)) + "|\n")}
		case args[0] == "volume" && args[1] == "rm":
			steps = append(steps, "rm volume "+args[len(args)-1])
		case args[0] == "inspect" && args[len(args)-1] == legacyContainer:
			return dockerResult{stdout: []byte("id\n")}
		case args[0] == "inspect":
			return boxInspectReply(BuiltinBoxImage(), "c1", "s")
		case args[0] == "exec" && args[1] == legacyContainer:
			if hasRuns {
				return dockerResult{}
			}
			return dockerResult{code: 2}
		case args[0] == "rm":
			steps = append(steps, "rm container "+args[len(args)-1])
		case args[0] == "run" && slices.Contains(args, boxLegacyCopyScript):
			steps = append(steps, "copy")
			if !strings.Contains(joined, "--volume "+legacyVolume+":/old:ro") || !strings.Contains(joined, "--volume diana-sandbox:/new") || !strings.Contains(joined, "--network none") {
				t.Errorf("copy container: %s", joined)
			}
		}
		return dockerResult{}
	}}
	previous := sharedBox.c
	sharedBox.mu.Lock()
	sharedBox.c = newBoxContainer(testBoxConfig(), fake.run)
	sharedBox.mu.Unlock()
	t.Cleanup(func() {
		sharedBox.mu.Lock()
		sharedBox.c = previous
		sharedBox.mu.Unlock()
	})
	if !migrateLegacyBoxes(context.Background(), fake.run) || len(steps) != 0 {
		t.Fatalf("busy legacy box must wait: steps=%v", steps)
	}
	hasRuns = false
	if migrateLegacyBoxes(context.Background(), fake.run) {
		t.Fatal("migration should be complete")
	}
	if want := []string{"rm container " + legacyContainer, "copy", "rm volume " + legacyVolume}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	copyCall := fake.calls[slices.IndexFunc(fake.calls, func(c []string) bool { return slices.Contains(c, boxLegacyCopyScript) })]
	b, _ := BoxFor(key)
	if got := copyCall[len(copyCall)-2:]; got[0] != b.scope || got[1] != strconv.Itoa(b.uid) {
		t.Fatalf("copy target = %v, want %s %d", got, b.scope, b.uid)
	}
}

// 命令里老习惯的 /work、/work/<路径> 落到本会话目录；已经带范围的、别处的 work 目录不动。
func TestScopeLegacyWorkPaths(t *testing.T) {
	root := BoxWorkDir + "/s-0123456789abcdef"
	for in, want := range map[string]string{
		`ffmpeg -y -i /work/downloads/a.mp4 -loop 0 /work/out.gif`: `ffmpeg -y -i ` + root + `/downloads/a.mp4 -loop 0 ` + root + `/out.gif`,
		`cd /work && ls`:                              `cd ` + root + ` && ls`,
		`cat "/work/x.txt" '/work/y'`:                 `cat "` + root + `/x.txt" '` + root + `/y'`,
		`ls ` + root + `/downloads`:                   `ls ` + root + `/downloads`,
		`ls /srv/u/work/a ./work/b /workspace work/c`: `ls /srv/u/work/a ./work/b /workspace work/c`,
		`x=/work/a.txt; echo $x`:                      `x=` + root + `/a.txt; echo $x`,
		`ffmpeg -i downloads/a.mp4 out.gif`:           `ffmpeg -i downloads/a.mp4 out.gif`,
	} {
		if got := scopeLegacyWorkPaths(in, root); got != want {
			t.Errorf("scopeLegacyWorkPaths(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
