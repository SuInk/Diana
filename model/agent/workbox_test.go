// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"
	"slices"
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
	for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10"} {
		if !strings.Contains(boxFirewallScript, cidr) {
			t.Fatalf("firewall must reject %s", cidr)
		}
	}
	if !strings.Contains(boxFirewallScript, "-o lo -j ACCEPT") {
		t.Fatal("firewall must keep loopback for Docker's embedded DNS")
	}
	b := newWorkBox(testBoxConfig(), nil, "k")
	args := strings.Join(b.firewallArgs(), " ")
	for _, want := range []string{"--rm", "--network container:" + b.container, "--cap-drop ALL", "--cap-add NET_ADMIN"} {
		if !strings.Contains(args, want) {
			t.Fatalf("firewall args missing %q: %s", want, args)
		}
	}
}

func TestBoxEnsureLifecycle(t *testing.T) {
	running := false
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			if running {
				return dockerResult{stdout: []byte("true " + BuiltinBoxImage() + "\n")}
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
	want := []string{"inspect", "image inspect", "image inspect", "build", "network inspect", "network create", "volume create", "run", "exec", "run firewall"}
	if got := fake.verbs(); !slices.Equal(got, want) {
		t.Fatalf("first ensure:\n got %v\nwant %v", got, want)
	}
	// 同一进程里第二次使用：容器在跑、镜像对、防火墙已写，只 inspect。
	fake.calls = nil
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.verbs(); !slices.Equal(got, []string{"inspect"}) {
		t.Fatalf("warm ensure: %v", got)
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
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch {
		case args[0] == "inspect":
			return dockerResult{code: 1}
		case args[0] == "run" && slices.Contains(args, "--rm"):
			return dockerResult{code: 1, stderr: "iptables: permission denied"}
		}
		return dockerResult{}
	}}
	b := newWorkBox(testBoxConfig(), fake.run, "k")
	if err := b.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "防火墙") {
		t.Fatalf("want firewall error, got %v", err)
	}
	if got := fake.verbs(); got[len(got)-1] != "rm" {
		t.Fatalf("networked box without firewall must be removed: %v", got)
	}
}

func TestBoxEnsureRecreatesOnImageChange(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "inspect" {
			return dockerResult{stdout: []byte("true diana-box:old\n")}
		}
		return dockerResult{}
	}}
	b := newWorkBox(testBoxConfig(), fake.run, "k")
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
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

func TestBoxExecTimeoutAndCancel(t *testing.T) {
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return dockerResult{stdout: []byte("true x\n")}
		case "exec":
			if slices.Contains(args, boxExecScript) {
				if args[len(args)-2] != "600" {
					t.Errorf("timeout must be capped at 600s, got %s", args[len(args)-2])
				}
				return dockerResult{code: 124, stdout: []byte("partial\n")}
			}
		}
		return dockerResult{}
	}}
	b := newWorkBox(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, fake.run, "k")
	var out bytes.Buffer
	res, err := b.Exec(context.Background(), "sleep 9999", time.Hour, &out)
	if err != nil || !res.TimedOut || out.String() != "partial\n" {
		t.Fatalf("res=%+v err=%v out=%q", res, err, out.String())
	}
}

func TestBoxExecRecreatesVanishedContainerOnce(t *testing.T) {
	attempts := 0
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return dockerResult{stdout: []byte("true x\n")}
		case "exec":
			if slices.Contains(args, boxExecScript) {
				attempts++
				if attempts == 1 {
					return dockerResult{code: 1, stderr: "Error response from daemon: No such container: diana-box-x"}
				}
				return dockerResult{stdout: []byte("ok\n")}
			}
		}
		return dockerResult{}
	}}
	b := newWorkBox(BoxConfig{Image: "x", Memory: "1g", CPUs: 1, PIDs: 64}, fake.run, "k")
	var out bytes.Buffer
	if res, err := b.Exec(context.Background(), "true", 0, &out); err != nil || res.ExitCode != 0 || attempts != 2 {
		t.Fatalf("res=%+v err=%v attempts=%d", res, err, attempts)
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
		goos, info string
		ok         bool
	}{
		{"linux", `["name=seccomp,profile=builtin","name=rootless"]|Ubuntu 24.04`, true},
		{"linux", `["name=seccomp,profile=builtin"]|Ubuntu 24.04`, false},
		{"android", `["name=seccomp"]|Debian`, false},
		{"linux", `["name=seccomp"]|Docker Desktop`, true},
		{"darwin", `["name=seccomp,profile=builtin","name=cgroupns"]|Docker Desktop`, true},
		{"darwin", `["name=seccomp"]|Ubuntu 24.04 (colima)`, true},
	}
	for _, c := range cases {
		if got := boxDaemonPolicy(c.goos, c.info) == ""; got != c.ok {
			t.Fatalf("%s %s: ok=%v", c.goos, c.info, got)
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
		var out bytes.Buffer
		res, err := b.Exec(ctx, command, timeout, &out)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		return res, out.String()
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
	if _, out := run(`wc -l < /work/.output/*.log`, 0); strings.TrimSpace(out) != "300000" {
		t.Fatalf("spilled output lines = %q", out)
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
	if idle.firewalled {
		t.Fatal("stopped box must re-apply its firewall next time")
	}
}
