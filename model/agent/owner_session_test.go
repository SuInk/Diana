package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ownerSessionShell(t *testing.T) OwnerCommand {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix fixture")
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	return OwnerCommand{Command: shell, Args: []string{}, CWD: cwd}
}

func newTestOwnerSessions(t *testing.T) *OwnerSessions {
	t.Helper()
	m := NewOwnerSessions()
	t.Cleanup(m.Close)
	return m
}

// 一个 shell 会话能来回输入、读到输出，退出码也能拿回来。
func TestOwnerSessionRoundTrip(t *testing.T) {
	m := newTestOwnerSessions(t)
	started, err := m.Start(Config{}, "bot", ownerSessionShell(t), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Running || started.SessionID == "" {
		t.Fatalf("Start() = %+v", started)
	}
	got, err := m.Send("bot", started.SessionID, "echo hello-$((40+2))", 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Output, "hello-42") {
		t.Fatalf("Send() output = %q", got.Output)
	}
	// 读过的不再重复给。
	again, err := m.Read("bot", started.SessionID, 0, nil)
	if err != nil || again.Output != "" {
		t.Fatalf("Read() = %+v, %v; want no repeated output", again, err)
	}
	if _, err := m.Send("bot", started.SessionID, "exit 3", time.Second, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		final, err := m.Read("bot", started.SessionID, 200*time.Millisecond, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !final.Running {
			if final.ExitCode == nil || *final.ExitCode != 3 || final.EndReason != "exited" {
				t.Fatalf("final = %+v", final)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session did not exit")
		}
	}
	// 结束且读完的会话就此清掉。
	if _, err := m.Read("bot", started.SessionID, 0, nil); err != ErrOwnerSessionNotFound {
		t.Fatalf("Read() after drain err = %v", err)
	}
}

// 别的机器人看不到、也碰不了这个会话。
func TestOwnerSessionIsolatedByOwner(t *testing.T) {
	m := newTestOwnerSessions(t)
	started, err := m.Start(Config{}, "bot-a", ownerSessionShell(t), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Send("bot-b", started.SessionID, "echo hi", 0, nil); err != ErrOwnerSessionNotFound {
		t.Fatalf("Send() from other owner err = %v", err)
	}
	if _, err := m.Stop("bot-b", started.SessionID); err != ErrOwnerSessionNotFound {
		t.Fatalf("Stop() from other owner err = %v", err)
	}
	if list := m.List("bot-b"); len(list) != 0 {
		t.Fatalf("List() for other owner = %+v", list)
	}
	if list := m.List("bot-a"); len(list) != 1 {
		t.Fatalf("List() = %+v", list)
	}
}

func TestOwnerSessionLimitsRunningSessions(t *testing.T) {
	m := newTestOwnerSessions(t)
	shell := ownerSessionShell(t)
	ids := []string{}
	for range OwnerSessionMaxRunning {
		started, err := m.Start(Config{}, "bot", shell, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, started.SessionID)
	}
	if _, err := m.Start(Config{}, "bot", shell, 0, nil); err == nil {
		t.Fatal("Start() beyond the limit succeeded")
	}
	// 另一个机器人有自己的名额。
	if _, err := m.Start(Config{}, "other", shell, 0, nil); err != nil {
		t.Fatalf("Start() for another owner: %v", err)
	}
	if _, err := m.Stop("bot", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(Config{}, "bot", shell, 0, nil); err != nil {
		t.Fatalf("Start() after stopping one: %v", err)
	}
}

// 输出多过缓冲时丢最早的，并说清丢了多少；一次读不完的分页给。
func TestOwnerSessionBoundsBufferedOutput(t *testing.T) {
	m := newTestOwnerSessions(t)
	shell := ownerSessionShell(t)
	shell.Args = []string{"-c", "i=0; while [ $i -lt 6000 ]; do printf '%063d\\n' $i; i=$((i+1)); done; sleep 30"}
	started, err := m.Start(Config{}, "bot", shell, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 等它把 6000×64 字节写完。
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, _ := m.get("bot", started.SessionID)
		s.mu.Lock()
		total := s.base + int64(len(s.data))
		s.mu.Unlock()
		if total >= 6000*64 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d bytes written", total)
		}
		time.Sleep(20 * time.Millisecond)
	}
	first, err := m.Read("bot", started.SessionID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dropped != 6000*64-ownerSessionBufferBytes || len(first.Output) != ownerSessionReadBytes || !first.More {
		t.Fatalf("first read dropped=%d len=%d more=%v", first.Dropped, len(first.Output), first.More)
	}
	second, err := m.Read("bot", started.SessionID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Dropped != 0 || len(second.Output) != ownerSessionReadBytes {
		t.Fatalf("second read dropped=%d len=%d", second.Dropped, len(second.Output))
	}
}

// 没人操作的会话到点就收，进程一并结束。
func TestOwnerSessionReapsIdleSessions(t *testing.T) {
	m := newOwnerSessions(50*time.Millisecond, time.Hour, 10*time.Millisecond)
	t.Cleanup(m.Close)
	shell := ownerSessionShell(t)
	shell.Args = []string{"-c", "sleep 30"}
	started, err := m.Start(Config{}, "bot", shell, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.get("bot", started.SessionID)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle session was not killed")
	}
	if list := m.List("bot"); len(list) != 0 {
		t.Fatalf("List() after reap = %+v", list)
	}
	s.mu.Lock()
	reason := s.endReason
	s.mu.Unlock()
	if reason != "idle_timeout" {
		t.Fatalf("endReason = %q", reason)
	}
}

// Diana 停止时结束全部会话，之后不再接新会话。
func TestOwnerSessionCloseStopsEverything(t *testing.T) {
	m := NewOwnerSessions()
	shell := ownerSessionShell(t)
	shell.Args = []string{"-c", "sleep 30"}
	started, err := m.Start(Config{}, "bot", shell, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.get("bot", started.SessionID)
	m.Close()
	select {
	case <-s.done:
	default:
		t.Fatal("Close() returned with the session still running")
	}
	if _, err := m.Start(Config{}, "bot", shell, 0, nil); err == nil {
		t.Fatal("Start() after Close succeeded")
	}
}

func TestOwnerSessionRejectsChangedPath(t *testing.T) {
	m := newTestOwnerSessions(t)
	shell := ownerSessionShell(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "sh-link")
	if err := os.Symlink(shell.Command, link); err != nil {
		t.Skip(err)
	}
	shell.Command = link
	if _, err := m.Start(Config{}, "bot", shell, 0, nil); err == nil {
		t.Fatal("Start() accepted an unresolved symlink")
	}
}
