package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/internal/procgroup"
)

// 主人在宿主机上的交互会话：RunOwnerCommand 一次跑完就收，跑不了 dev server、
// REPL、要来回输入的安装程序这类得一直开着的进程。会话把进程留在后台，输出攒进
// 一段有上限的缓冲，模型分几次读、往 stdin 里写。
//
// 用管道不用 PTY：不引新依赖、Windows 也能用；代价是 top、vim 这类非得要终端的
// 程序跑不了，它们本来也不适合给模型操作。
//
// 会话不是常驻进程：空闲太久、活得太长都会被收掉，Diana 停止时全部结束。审核放在
// 调用方（assistant 包），这里只管进程本身。

const (
	OwnerSessionMaxRunning   = 4
	OwnerSessionIdleTimeout  = 30 * time.Minute
	OwnerSessionMaxLifetime  = 4 * time.Hour
	OwnerSessionMaxWait      = 15 * time.Second
	ownerSessionBufferBytes  = 256 << 10
	ownerSessionReadBytes    = 64 << 10
	ownerSessionSettle       = 300 * time.Millisecond
	ownerSessionInputBytes   = 16 << 10
	ownerSessionStopWait     = 5 * time.Second
	ownerSessionReapInterval = 30 * time.Second
)

var ErrOwnerSessionNotFound = errors.New("会话不存在或已结束清理，用 session_list 查看现有会话")

// OwnerSessions 管一个机器人名下的全部会话。零值不可用，用 NewOwnerSessions。
type OwnerSessions struct {
	mu       sync.Mutex
	sessions map[string]*ownerSession
	closed   bool

	idle     time.Duration
	lifetime time.Duration
	now      func() time.Time
	stopReap chan struct{}
}

func NewOwnerSessions() *OwnerSessions {
	return newOwnerSessions(OwnerSessionIdleTimeout, OwnerSessionMaxLifetime, ownerSessionReapInterval)
}

func newOwnerSessions(idle, lifetime, reapEvery time.Duration) *OwnerSessions {
	m := &OwnerSessions{
		sessions: map[string]*ownerSession{},
		idle:     idle,
		lifetime: lifetime,
		now:      time.Now,
		stopReap: make(chan struct{}),
	}
	go m.reapLoop(reapEvery)
	return m
}

type ownerSession struct {
	id      string
	owner   string
	request OwnerCommand
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started time.Time
	done    chan struct{}

	mu         sync.Mutex
	data       []byte
	base       int64 // data[0] 在整段输出里的偏移
	delivered  int64 // 已经交给模型的位置
	changed    chan struct{}
	lastActive time.Time
	exitCode   int
	exitErr    string
	endReason  string
}

// OwnerSessionResult 是每次会话操作返回给模型的快照。Output 只含上次读取之后的新输出。
type OwnerSessionResult struct {
	SessionID string       `json:"session_id"`
	Command   OwnerCommand `json:"command"`
	Running   bool         `json:"running"`
	ExitCode  *int         `json:"exit_code,omitempty"`
	Error     string       `json:"error,omitempty"`
	EndReason string       `json:"end_reason,omitempty"`
	Output    string       `json:"output"`
	// Dropped 是缓冲装不下、没来得及读就被挤掉的字节数。
	Dropped int64 `json:"dropped_bytes,omitempty"`
	// More 表示这次只给了一部分，剩下的再 session_read。
	More      bool      `json:"more_output,omitempty"`
	StartedAt time.Time `json:"started_at"`
	IdleLimit string    `json:"idle_limit"`
	Lifetime  string    `json:"max_lifetime"`
}

// Start 校验并启动一条会话命令，等 wait 时长收第一段输出。request 必须是已经过
// ValidateOwnerCommand 和审核的那一份，这里再校验一遍，防止中间路径被换掉。
func (m *OwnerSessions) Start(cfg Config, owner string, request OwnerCommand, wait time.Duration, cancel <-chan struct{}) (OwnerSessionResult, error) {
	validated, err := ValidateOwnerCommand(cfg, request)
	if err != nil {
		return OwnerSessionResult{}, err
	}
	if !ownerCommandsEqual(request, validated) {
		return OwnerSessionResult{}, fmt.Errorf("执行路径已改变，请重新检查命令")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return OwnerSessionResult{}, fmt.Errorf("Diana 正在停止，不能再开会话")
	}
	running := 0
	for _, s := range m.sessions {
		if s.owner == owner && s.running() {
			running++
		}
	}
	if running >= OwnerSessionMaxRunning {
		m.mu.Unlock()
		return OwnerSessionResult{}, fmt.Errorf("同时最多 %d 个运行中的会话，先 session_stop 不用的", OwnerSessionMaxRunning)
	}
	cmd := procgroup.Isolate(exec.Command(request.Command, request.Args...))
	cmd.Dir = request.CWD
	cmd.Env = ownerCommandEnv()
	s := &ownerSession{
		id:      newOwnerSessionID(),
		owner:   owner,
		request: request,
		cmd:     cmd,
		started: m.now(),
		done:    make(chan struct{}),
		changed: make(chan struct{}),
	}
	s.lastActive = s.started
	cmd.Stdout = s
	cmd.Stderr = s
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.mu.Unlock()
		return OwnerSessionResult{}, err
	}
	s.stdin = stdin
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return OwnerSessionResult{}, err
	}
	m.sessions[s.id] = s
	m.mu.Unlock()
	go s.wait()
	s.waitOutput(wait, cancel)
	return m.snapshot(s), nil
}

// Send 往会话的 stdin 写一段输入（末尾没有换行时补一个），再等 wait 收输出。
func (m *OwnerSessions) Send(owner, id, input string, wait time.Duration, cancel <-chan struct{}) (OwnerSessionResult, error) {
	s, err := m.get(owner, id)
	if err != nil {
		return OwnerSessionResult{}, err
	}
	if len(input) > ownerSessionInputBytes {
		return OwnerSessionResult{}, fmt.Errorf("一次输入最多 %d 字节", ownerSessionInputBytes)
	}
	if !s.running() {
		return OwnerSessionResult{}, fmt.Errorf("会话已经结束，不能再输入；用 session_read 取剩余输出")
	}
	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}
	s.touch(m.now())
	if _, err := io.WriteString(s.stdin, input); err != nil {
		return OwnerSessionResult{}, fmt.Errorf("写入会话失败：%w", err)
	}
	s.waitOutput(wait, cancel)
	return m.snapshot(s), nil
}

// Read 取上次之后的新输出；wait > 0 时没有新输出会等一会儿。
func (m *OwnerSessions) Read(owner, id string, wait time.Duration, cancel <-chan struct{}) (OwnerSessionResult, error) {
	s, err := m.get(owner, id)
	if err != nil {
		return OwnerSessionResult{}, err
	}
	s.touch(m.now())
	s.waitOutput(wait, cancel)
	result := m.snapshot(s)
	// 已经结束、输出也读完了的会话就此清掉，不必等空闲超时。
	if !result.Running && !result.More {
		m.remove(id)
	}
	return result, nil
}

// Stop 结束会话的整个进程组，返回剩下没读的输出。
func (m *OwnerSessions) Stop(owner, id string) (OwnerSessionResult, error) {
	s, err := m.get(owner, id)
	if err != nil {
		return OwnerSessionResult{}, err
	}
	s.terminate("stopped")
	result := m.snapshot(s)
	m.remove(id)
	return result, nil
}

// List 列出 owner 名下的会话，不推进读取位置。
func (m *OwnerSessions) List(owner string) []OwnerSessionResult {
	m.mu.Lock()
	sessions := make([]*ownerSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.owner == owner {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	out := make([]OwnerSessionResult, 0, len(sessions))
	for _, s := range sessions {
		result := m.describe(s)
		out = append(out, result)
	}
	return out
}

// Close 结束全部会话，之后不再接受新会话。Diana 停止时调用。
func (m *OwnerSessions) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stopReap)
	sessions := make([]*ownerSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*ownerSession{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *ownerSession) {
			defer wg.Done()
			s.terminate("diana_stopped")
		}(s)
	}
	wg.Wait()
}

func (m *OwnerSessions) get(owner, id string) (*ownerSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[strings.TrimSpace(id)]
	// 别的机器人的会话一律当作不存在，不透露它在跑。
	if !ok || s.owner != owner {
		return nil, ErrOwnerSessionNotFound
	}
	return s, nil
}

func (m *OwnerSessions) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *OwnerSessions) reapLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopReap:
			return
		case <-ticker.C:
			m.reap()
		}
	}
}

// reap 收掉空闲超时和活得太久的会话。空闲按最后一次被操作算，不按有没有输出：
// 一直刷日志的 dev server 没人看也该关。
func (m *OwnerSessions) reap() {
	now := m.now()
	m.mu.Lock()
	var expired []*ownerSession
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := now.Sub(s.lastActive) >= m.idle
		s.mu.Unlock()
		old := now.Sub(s.started) >= m.lifetime
		if idle || old {
			expired = append(expired, s)
			delete(m.sessions, id)
			reason := "idle_timeout"
			if old {
				reason = "max_lifetime"
			}
			s.mu.Lock()
			if s.endReason == "" {
				s.endReason = reason
			}
			s.mu.Unlock()
		}
	}
	m.mu.Unlock()
	for _, s := range expired {
		s.terminate("")
	}
}

func (m *OwnerSessions) snapshot(s *ownerSession) OwnerSessionResult {
	result := m.describe(s)
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.delivered
	if start < s.base {
		result.Dropped = s.base - start
		start = s.base
	}
	end := s.base + int64(len(s.data))
	chunk := s.data[start-s.base : end-s.base]
	if len(chunk) > ownerSessionReadBytes {
		chunk = chunk[:ownerSessionReadBytes]
		// 别把一个多字节字符切成两半：末尾是残缺的字符就退回去，留给下一次读。
		for i := 0; i < utf8.UTFMax-1 && len(chunk) > 0; i++ {
			if r, size := utf8.DecodeLastRune(chunk); r != utf8.RuneError || size > 1 {
				break
			}
			chunk = chunk[:len(chunk)-1]
		}
		result.More = true
	}
	s.delivered = start + int64(len(chunk))
	result.Output = strings.ToValidUTF8(string(chunk), "�")
	return result
}

func (m *OwnerSessions) describe(s *ownerSession) OwnerSessionResult {
	result := OwnerSessionResult{
		SessionID: s.id,
		Command:   s.request,
		StartedAt: s.started,
		IdleLimit: m.idle.String(),
		Lifetime:  m.lifetime.String(),
		Running:   s.running(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !result.Running {
		code := s.exitCode
		result.ExitCode = &code
		result.Error = s.exitErr
		result.EndReason = s.endReason
		if result.EndReason == "" {
			result.EndReason = "exited"
		}
	}
	if s.delivered < s.base+int64(len(s.data)) {
		result.More = true
	}
	return result
}

// Write 收进程输出。缓冲满了从头丢：最新的输出最有用（报错、提示符都在最后）。
func (s *ownerSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.data = append(s.data, p...)
	if over := len(s.data) - ownerSessionBufferBytes; over > 0 {
		s.data = append([]byte(nil), s.data[over:]...)
		s.base += int64(over)
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return len(p), nil
}

func (s *ownerSession) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.exitCode = 0
	if err != nil {
		s.exitCode = -1
		if s.cmd.ProcessState != nil {
			s.exitCode = s.cmd.ProcessState.ExitCode()
		}
		s.exitErr = err.Error()
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	// 主进程退出后它起的子进程可能还在组里，一并收掉。
	_ = procgroup.Kill(s.cmd)
	close(s.done)
}

func (s *ownerSession) running() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *ownerSession) touch(now time.Time) {
	s.mu.Lock()
	s.lastActive = now
	s.mu.Unlock()
}

func (s *ownerSession) terminate(reason string) {
	if reason != "" {
		s.mu.Lock()
		if s.endReason == "" {
			s.endReason = reason
		}
		s.mu.Unlock()
	}
	_ = s.stdin.Close()
	_ = procgroup.Kill(s.cmd)
	select {
	case <-s.done:
	case <-time.After(ownerSessionStopWait):
	}
}

// waitOutput 等到有新输出或进程结束，再多等一小段让同一批输出收齐，免得一条
// 提示只读到半句。wait 为 0 时不等。
func (s *ownerSession) waitOutput(wait time.Duration, cancel <-chan struct{}) {
	if wait <= 0 {
		return
	}
	wait = min(wait, OwnerSessionMaxWait)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	got := false
	for {
		s.mu.Lock()
		pending := s.delivered < s.base+int64(len(s.data))
		changed := s.changed
		s.mu.Unlock()
		if !s.running() {
			return
		}
		if pending {
			got = true
		}
		var settle <-chan time.Time
		if got {
			settle = time.After(ownerSessionSettle)
		}
		select {
		case <-changed:
		case <-settle:
			return
		case <-deadline.C:
			return
		case <-cancel:
			return
		}
	}
}

func newOwnerSessionID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "s-" + hex.EncodeToString(b[:])
}
