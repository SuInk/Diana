// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
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

func TestBoxRunArgsMatchYujiaxueIsolation(t *testing.T) {
	b := newWorkBox(testBoxConfig(), nil, "bot\x00group:1")
	args := strings.Join(b.runArgs(), " ")
	for _, want := range []string{
		"--user 1000:1000", "--cap-drop ALL", "--security-opt no-new-privileges", "--read-only",
		"--network " + boxNetworkName, "--env HOME=/work/.home", "--volume " + b.volume + ":/work",
		"--tmpfs /tmp:rw,nosuid,nodev,size=256m", "--memory 1g", "--cpus 2", "--pids-limit 256",
		"--workdir /work", "--hostname box", "--init", b.cfg.Image + " sleep infinity",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("run args missing %q:\n%s", want, args)
		}
	}
	// 容器里不能有机密：不挂宿主目录，除 HOME 外不传环境变量。
	for i, arg := range b.runArgs() {
		if arg == "--volume" && !strings.HasPrefix(b.runArgs()[i+1], "diana-work-") {
			t.Fatalf("box must only mount its named volume, got %q", b.runArgs()[i+1])
		}
		if arg == "--env" && !strings.HasPrefix(b.runArgs()[i+1], "HOME=") {
			t.Fatalf("box must not receive env %q", b.runArgs()[i+1])
		}
		if strings.Contains(arg, "docker.sock") {
			t.Fatal("box must never see a docker socket")
		}
	}
	offline := newWorkBox(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, nil, "k")
	if !strings.Contains(strings.Join(offline.runArgs(), " "), "--network none") {
		t.Fatal("network=false must use --network none")
	}
}

func TestBoxFirewallBlocksHostAndPrivateRanges(t *testing.T) {
	previous := boxHostAddresses
	boxHostAddresses = func() ([]string, []string) { return []string{"203.0.113.7/32"}, []string{"2001:db8::7/128"} }
	t.Cleanup(func() { boxHostAddresses = previous })
	b := newWorkBox(testBoxConfig(), nil, "k")
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

// running 的容器在 fake inspect 里的样子：运行中、镜像、ID、启动时间。
func boxInspectReply(image, id, started string) dockerResult {
	return dockerResult{stdout: []byte("true " + image + " " + id + " " + started + "\n")}
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
	b := newWorkBox(testBoxConfig(), fake.run, "k")
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 防火墙在 mkdir 之前写：容器里还没跑过任何东西时出站就已经收紧。
	want := []string{"inspect", "image inspect", "image inspect", "build", "network inspect", "network create", "volume create", "run", "inspect", "run firewall", "exec"}
	if got := fake.verbs(); !slices.Equal(got, want) {
		t.Fatalf("first ensure:\n got %v\nwant %v", got, want)
	}
	// 同一进程里第二次使用：容器还是写防火墙时那个，只 inspect。
	fake.calls = nil
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect"}) {
		t.Fatalf("warm ensure: %v", got)
	}
	// 容器被外部重启（ID 不变、启动时间变了）：网络命名空间是新的，规则要重写。
	started = "2026-11-15T02:00:00Z"
	fake.calls = nil
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "run firewall"}) {
		t.Fatalf("restarted container must get its firewall again: %v", got)
	}
	// 新进程接手旧容器：不知道防火墙写没写，重写一遍。
	fresh := newWorkBox(testBoxConfig(), fake.run, "k")
	fake.calls = nil
	if err := fresh.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect", "run firewall"}) {
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
	b := newWorkBox(testBoxConfig(), fake.run, "k")
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
	b := newWorkBox(testBoxConfig(), fake.run, "k")
	_ = b.Ensure(context.Background())
	if got := fake.verbs(); len(got) < 2 || got[1] != "rm" {
		t.Fatalf("stale image must be replaced: %v", got)
	}
}

func TestBoxResetRemovesContainerAndVolume(t *testing.T) {
	fake := &fakeDocker{reply: func([]string, []byte) dockerResult { return dockerResult{} }}
	b := newWorkBox(testBoxConfig(), fake.run, "k")
	if err := b.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"rm", "volume rm"}) {
		t.Fatalf("reset: %v", got)
	}
}

func offlineTestBox(fake *fakeDocker) *WorkBox {
	return newWorkBox(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, fake.run, "k")
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
				if got := args[len(args)-4]; args[len(args)-3] == "sleep 9999" && got != "600" {
					t.Errorf("timeout must be capped at 600s, got %s", got)
				}
				if out := args[len(args)-2]; !strings.HasPrefix(out, boxOutputDir+"/") || !strings.HasSuffix(out, ".log") {
					t.Errorf("full output must stay in the box, got %s", out)
				}
				return dockerResult{code: code}
			}
		}
		return dockerResult{}
	}}
	b := offlineTestBox(fake)
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
					return dockerResult{code: 1, stderr: "Error response from daemon: No such container: diana-box-x"}
				}
			}
		}
		return dockerResult{}
	}}
	if res, err := offlineTestBox(fake).Exec(context.Background(), "true", 0); err != nil || res.ExitCode != 0 || attempts != 2 {
		t.Fatalf("res=%+v err=%v attempts=%d", res, err, attempts)
	}
}

// 输出读回只要尾部窗口：头一行是总字节和总行数，后面是窗口内容，起点按两者推出来。
func TestBoxOutputTail(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "exec" && slices.Contains(args, boxOutputTailScript) {
			if args[len(args)-1] != "8" {
				t.Errorf("window = %s", args[len(args)-1])
			}
			return dockerResult{stdout: []byte("100 12\nline-11\n")}
		}
		return dockerResult{}
	}}
	tail, start, size, lines, err := offlineTestBox(fake).OutputTail(context.Background(), "/work/.output/a.log", 8)
	if err != nil || string(tail) != "line-11\n" || start != 92 || size != 100 || lines != 12 {
		t.Fatalf("tail=%q start=%d size=%d lines=%d err=%v", tail, start, size, lines, err)
	}
}

// 同一个 key 并发取 box 只能得到同一个实例，否则两把锁会交错建容器、写防火墙。
func TestBoxForIsAtomic(t *testing.T) {
	previous := boxProbe
	boxProbe = func(BoxConfig) (dockerRunner, string) {
		return func(context.Context, dockerCall) dockerResult { return dockerResult{} }, ""
	}
	boxAvailability.mu.Lock()
	boxAvailability.checked = time.Time{}
	boxAvailability.mu.Unlock()
	t.Cleanup(func() {
		boxProbe = previous
		boxAvailability.mu.Lock()
		boxAvailability.checked = time.Time{}
		boxAvailability.mu.Unlock()
		boxRegistry.Delete("concurrent")
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
}

func TestNormalizeBoxPath(t *testing.T) {
	for in, want := range map[string]string{"a.txt": "/work/a.txt", "/work/x/./y": "/work/x/y", "/work": "/work", "inbox/1.png": "/work/inbox/1.png"} {
		if got, err := NormalizeBoxPath(in); err != nil || got != want {
			t.Fatalf("%q → %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "/work/../etc", "/workx/a", "a\nb"} {
		if _, err := NormalizeBoxPath(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestBoxCopyOutEnforcesLimit(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return dockerResult{stdout: []byte("true x\n")}
		case "exec":
			return dockerResult{stdout: []byte("/work/out.png\nPNGDATA")}
		}
		return dockerResult{}
	}}
	b := newWorkBox(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, fake.run, "k")
	real, data, err := b.CopyOut(context.Background(), "out.png", 1024)
	if err != nil || real != "/work/out.png" || string(data) != "PNGDATA" {
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

func TestLastWeeklyBoxBoundary(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	for now, want := range map[time.Time]time.Time{
		time.Date(2026, 11, 15, 12, 0, 0, 0, loc): time.Date(2026, 11, 9, 5, 0, 0, 0, loc),  // 周日
		time.Date(2026, 11, 16, 4, 59, 0, 0, loc): time.Date(2026, 11, 9, 5, 0, 0, 0, loc),  // 周一 05:00 前
		time.Date(2026, 11, 16, 5, 0, 0, 0, loc):  time.Date(2026, 11, 16, 5, 0, 0, 0, loc), // 周一 05:00
	} {
		if got := lastWeeklyBoxBoundary(now); !got.Equal(want) {
			t.Fatalf("%v → %v, want %v", now, got, want)
		}
	}
}

// TestBoxRealDocker 用真 Docker 跑一遍：联网、挡宿主回环和私网、超时杀进程组、文件进出、重置。
// 需要本机有 Docker 和构建好的内置镜像：DIANA_BOX_DOCKER_TEST=1 scripts/test-serial.sh ./model/agent
func TestBoxRealDocker(t *testing.T) {
	if os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		t.Skip("set DIANA_BOX_DOCKER_TEST=1 to run against a real Docker daemon")
	}
	docker, reason := probeBoxRuntime(BoxConfig{})
	if reason != "" {
		t.Fatal(reason)
	}
	b := newWorkBox(testBoxConfig(), docker, "diana-box-test\x00"+t.Name())
	ctx := context.Background()
	t.Cleanup(func() { _ = b.Reset(context.Background()) })
	run := func(command string, timeout time.Duration) (BoxExecResult, string) {
		t.Helper()
		res, err := b.Exec(ctx, command, timeout)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		tail, _, _, _, err := b.OutputTail(ctx, res.OutputPath, 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		return res, string(tail)
	}
	if res, out := run(`id -u; env | sort | cut -d= -f1 | tr '\n' ' '; touch /etc/x 2>&1 | head -1`, 0); res.ExitCode != 0 || !strings.HasPrefix(out, "1000\n") || !strings.Contains(out, "Read-only") {
		t.Fatalf("identity/readonly: %+v %q", res, out)
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
	if _, err := b.WriteFile(ctx, "inbox/hello.txt", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	if real, data, err := b.CopyOut(ctx, "inbox/hello.txt", 1024); err != nil || real != "/work/inbox/hello.txt" || string(data) != "hi" {
		t.Fatalf("copy out: %q %q %v", real, data, err)
	}
	run(`ln -sf /etc/passwd /work/leak`, 0)
	if _, _, err := b.CopyOut(ctx, "leak", 1<<20); err == nil {
		t.Fatal("symlink escaping /work must be rejected")
	}
	// 输出放不下时给尾部整行，完整输出落进 box，模型在容器里按行读。
	body, err := RunBoxCommand(ctx, b, `seq 1 300000`, 0)
	if err != nil || !strings.Contains(body, `"truncated": true`) || !strings.Contains(body, "300000") || !strings.Contains(body, "/work/.output/") {
		t.Fatalf("paged output: err=%v %.400s", err, body)
	}
	var paged struct {
		FullOutputPath string `json:"full_output_path"`
	}
	_ = json.Unmarshal([]byte(body), &paged)
	if _, out := run(`wc -l < `+paged.FullOutputPath, 0); strings.TrimSpace(out) != "300000" {
		t.Fatalf("full output lines in %q = %q", paged.FullOutputPath, out)
	}
	// 完整输出只留最新几份。
	for range boxOutputKeep + 5 {
		run(`true`, 0)
	}
	if _, out := run(`ls /work/.output | wc -l`, 0); strings.TrimSpace(out) != strconv.Itoa(boxOutputKeep) {
		t.Fatalf("output rotation kept %q files", out)
	}
	// 容器被外部重启后网络命名空间是新的：下次使用前必须重写防火墙。
	if res := docker(ctx, dockerCall{args: []string{"restart", "--time", "1", b.container}, timeout: time.Minute}); res.code != 0 {
		t.Fatalf("restart: %s", res.stderr)
	}
	if _, out := run(`curl -s -m 5 -o /dev/null -w '%{http_code}' http://host.docker.internal:18080/; echo " $?"`, 0); !strings.HasPrefix(out, "000 ") {
		t.Fatalf("firewall lost after external restart: %q", out)
	}
	if _, err := RunBoxCommand(ctx, b, `echo boom >&2; exit 3`, 0); err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), `"exit_code": 3`) {
		t.Fatalf("failing command: %v", err)
	}
	if err := b.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, out := run(`ls /work/inbox`, 0); strings.TrimSpace(out) != "" {
		t.Fatalf("reset must wipe the volume, got %q", out)
	}
}

// 空闲回收：本进程用过的按最近使用时间，忙着的不动；上一次运行留下的按启动时间，
// 里面还有命令在跑时不动。回收只删容器，不删卷。
func TestStopIdleBoxes(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cfg := BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}
	fresh := newWorkBox(cfg, nil, "fresh")
	fresh.lastUsed.Store(now.Add(-time.Minute).UnixNano())
	idle := newWorkBox(cfg, nil, "idle")
	idle.lastUsed.Store(now.Add(-20 * time.Minute).UnixNano())
	busy := newWorkBox(cfg, nil, "busy")
	busy.lastUsed.Store(now.Add(-time.Hour).UnixNano())
	busy.running.Add(1)
	for _, b := range []*WorkBox{fresh, idle, busy} {
		boxRegistry.Store(b.key, b)
	}
	t.Cleanup(func() {
		for _, k := range []string{"fresh", "idle", "busy"} {
			boxRegistry.Delete(k)
		}
	})
	orphans := map[string]struct {
		started time.Time
		running bool
	}{
		"diana-box-old":     {now.Add(-time.Hour), false},
		"diana-box-working": {now.Add(-time.Hour), true},
		"diana-box-new":     {now.Add(-time.Minute), false},
	}
	keys := map[string]string{fresh.container: "fresh", idle.container: "idle", busy.container: "busy", "diana-box-old": "old", "diana-box-working": "working", "diana-box-new": "new"}
	var removed []string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "ps":
			var names []string
			for name := range keys {
				names = append(names, name)
			}
			return dockerResult{stdout: []byte(strings.Join(names, "\n"))}
		case "inspect":
			name := args[len(args)-1]
			started := now.Add(-time.Hour)
			if o, ok := orphans[name]; ok {
				started = o.started
			}
			return dockerResult{stdout: []byte(started.Format(time.RFC3339Nano) + "|" + hex.EncodeToString([]byte(keys[name])))}
		case "exec":
			if orphans[args[1]].running {
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
	idle.docker = fake.run
	stopIdleBoxes(context.Background(), fake.run, now, BoxIdleTimeout)
	slices.Sort(removed)
	want := []string{"diana-box-old", idle.container}
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	if idle.firewalledFor != "" {
		t.Fatal("stopped box must re-apply its firewall next time")
	}
}

// 回收方在锁外粗筛之后、动手之前，可能有命令刚开始：StopIfIdle / ResetIfIdle 在锁内
// 再看一遍，忙着或刚用过就不动。
func TestBoxIdleActionsRecheckUnderLock(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var calls []string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		calls = append(calls, args[0])
		return dockerResult{}
	}}
	b := offlineTestBox(fake)
	b.lastUsed.Store(now.Add(-time.Hour).UnixNano())
	done := b.use() // 粗筛之后有命令开始了
	if b.StopIfIdle(context.Background(), now, BoxIdleTimeout) {
		t.Fatal("stopped a box with a running command")
	}
	if reset, _ := b.ResetIfIdle(context.Background()); reset {
		t.Fatal("weekly reset wiped a box with a running command")
	}
	done()
	if b.StopIfIdle(context.Background(), time.Now(), BoxIdleTimeout) {
		t.Fatal("stopped a box that was just used")
	}
	if len(calls) != 0 {
		t.Fatalf("busy box got docker calls: %v", calls)
	}
	if !b.StopIfIdle(context.Background(), time.Now().Add(BoxIdleTimeout+time.Minute), BoxIdleTimeout) {
		t.Fatal("idle box was not stopped")
	}
	if reset, err := b.ResetIfIdle(context.Background()); !reset || err != nil {
		t.Fatalf("idle box was not reset: %v", err)
	}
}
