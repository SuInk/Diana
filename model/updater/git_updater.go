package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrRemoteNotConfigured = errors.New("updater: git remote origin is not configured")

type Status struct {
	Root        string `json:"root"`
	Branch      string `json:"branch,omitempty"`
	RemoteName  string `json:"remote_name,omitempty"`
	RemoteURL   string `json:"remote_url,omitempty"`
	HeadCommit  string `json:"head_commit,omitempty"`
	HeadSubject string `json:"head_subject,omitempty"`
	// NearestTag 是 HEAD 可达的最近 tag，CommitsSinceTag 是从该 tag 到 HEAD 的提交数。
	NearestTag      string    `json:"nearest_tag,omitempty"`
	CommitsSinceTag int       `json:"commits_since_tag,omitempty"`
	Dirty           bool      `json:"dirty"`
	Ahead           int       `json:"ahead,omitempty"`
	Behind          int       `json:"behind,omitempty"`
	Upstream        string    `json:"upstream,omitempty"`
	LastFetchedAt   time.Time `json:"last_fetched_at,omitempty"`
	LastUpdateAt    time.Time `json:"last_update_at,omitempty"`
	LastUpdateText  string    `json:"last_update_text,omitempty"`
}

// VersionLabel 返回人类可读的版本号：正好在 tag 上显示 tag，落后若干提交
// 显示 tag+N，没有任何 tag 时回退为提交短号。
func (s Status) VersionLabel() string {
	if s.NearestTag != "" {
		if s.CommitsSinceTag > 0 {
			return fmt.Sprintf("%s+%d", s.NearestTag, s.CommitsSinceTag)
		}
		return s.NearestTag
	}
	return s.HeadCommit
}

type Result struct {
	Status  Status    `json:"status"`
	Fetched bool      `json:"fetched"`
	Updated bool      `json:"updated"`
	Output  string    `json:"output,omitempty"`
	At      time.Time `json:"at"`
}

// Settings 是系统更新的持久化配置。
type Settings struct {
	AutoUpdateEnabled bool `json:"auto_update_enabled"`
	IntervalMinutes   int  `json:"interval_minutes"`
}

// DefaultSettings 返回新安装使用的自动更新配置。
func DefaultSettings() Settings {
	return Settings{AutoUpdateEnabled: true, IntervalMinutes: 30}
}

// WithDefaults 补齐自动更新设置的默认值并收敛区间。
func (s Settings) WithDefaults() Settings {
	if s.IntervalMinutes <= 0 {
		s.IntervalMinutes = 30
	}
	if s.IntervalMinutes < 10 {
		// 过于频繁的自动拉取既没意义又容易触发远端限流。
		s.IntervalMinutes = 10
	}
	if s.IntervalMinutes > 1440 {
		s.IntervalMinutes = 1440
	}
	return s
}

type GitUpdater struct {
	root          string
	lastFetchedAt time.Time
	lastUpdateAt  time.Time
}

// NewGitUpdater 创建 Git 更新器。
func NewGitUpdater(root string) (*GitUpdater, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &GitUpdater{root: absRoot}, nil
}

// Status 读取当前 Git 仓库更新状态。
func (u *GitUpdater) Status(ctx context.Context) (Status, error) {
	status := Status{Root: u.root}

	// 状态接口尽量容错：单个 Git 信息读不到时保留空值，只有非退出类错误才返回。
	if branch, err := u.gitOutput(ctx, "branch", "--show-current"); err == nil {
		status.Branch = branch
	}
	if head, err := u.gitOutput(ctx, "rev-parse", "--short", "HEAD"); err == nil {
		status.HeadCommit = head
	}
	if subject, err := u.gitOutput(ctx, "log", "-1", "--pretty=%s"); err == nil {
		status.HeadSubject = subject
	}
	// describe --long 输出形如 v0.1.0-12-gc8e8432；仓库没有 tag 时该命令报错，保持空值即可。
	if describe, err := u.gitOutput(ctx, "describe", "--tags", "--long"); err == nil {
		if tag, count, ok := parseDescribe(describe); ok {
			status.NearestTag = tag
			status.CommitsSinceTag = count
		}
	}
	if dirty, err := u.gitOutput(ctx, "status", "--porcelain"); err == nil {
		status.Dirty = strings.TrimSpace(dirty) != ""
	}
	if remoteURL, err := u.gitOutput(ctx, "remote", "get-url", "origin"); err == nil {
		status.RemoteName = "origin"
		status.RemoteURL = remoteURL
	}
	if upstream, err := u.gitOutput(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		status.Upstream = upstream
	}
	if status.Upstream != "" {
		if aheadBehind, err := u.gitOutput(ctx, "rev-list", "--left-right", "--count", status.Upstream+"...HEAD"); err == nil {
			// rev-list 输出顺序是 behind ahead，因为左边是 upstream，右边是 HEAD。
			fmt.Sscanf(aheadBehind, "%d %d", &status.Behind, &status.Ahead)
		}
	}
	status.LastFetchedAt = u.lastFetchedAt
	status.LastUpdateAt = u.lastUpdateAt
	return status, nil
}

// Check 只 fetch 远端并刷新状态，不执行合并；用于"检查更新"。
func (u *GitUpdater) Check(ctx context.Context) (Status, error) {
	status, err := u.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	if status.RemoteURL == "" {
		return Status{}, ErrRemoteNotConfigured
	}
	if _, err := u.gitCombined(ctx, "fetch", "--prune", "origin"); err != nil {
		return Status{}, err
	}
	u.lastFetchedAt = time.Now()
	next, err := u.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	next.LastFetchedAt = u.lastFetchedAt
	return next, nil
}

// Update 执行 fetch 和 ff-only pull 更新。
func (u *GitUpdater) Update(ctx context.Context) (Result, error) {
	status, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if status.RemoteURL == "" {
		return Result{}, ErrRemoteNotConfigured
	}

	outputs := make([]string, 0, 2)
	// 先 fetch 再 ff-only pull，避免在 WebUI 更新时产生本地 merge commit。
	fetchOut, err := u.gitCombined(ctx, "fetch", "--prune", "origin")
	if err != nil {
		return Result{}, err
	}
	u.lastFetchedAt = time.Now()
	if trimmed := strings.TrimSpace(fetchOut); trimmed != "" {
		outputs = append(outputs, trimmed)
	}

	updateOut, err := u.gitCombined(ctx, "pull", "--ff-only", "origin", status.Branch)
	if err != nil {
		return Result{}, err
	}
	u.lastUpdateAt = time.Now()
	if trimmed := strings.TrimSpace(updateOut); trimmed != "" {
		outputs = append(outputs, trimmed)
	}

	nextStatus, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	nextStatus.LastFetchedAt = u.lastFetchedAt
	nextStatus.LastUpdateAt = u.lastUpdateAt
	result := Result{
		Status:  nextStatus,
		Fetched: true,
		Updated: !strings.Contains(updateOut, "Already up to date."),
		Output:  strings.TrimSpace(strings.Join(outputs, "\n\n")),
		At:      time.Now(),
	}
	nextStatus.LastUpdateText = result.Output
	result.Status = nextStatus
	return result, nil
}

// parseDescribe 解析 git describe --tags --long 的输出（tag-N-ghash）。
// tag 名本身可能含 '-'，所以从右往左拆两段。
func parseDescribe(describe string) (string, int, bool) {
	describe = strings.TrimSpace(describe)
	last := strings.LastIndex(describe, "-")
	if last <= 0 {
		return "", 0, false
	}
	rest := describe[:last]
	mid := strings.LastIndex(rest, "-")
	if mid <= 0 {
		return "", 0, false
	}
	count, err := strconv.Atoi(rest[mid+1:])
	if err != nil || count < 0 {
		return "", 0, false
	}
	return rest[:mid], count, true
}

// rollbackRefPattern 限制回退目标只能是提交号或 tag 这类安全字符，防止参数注入。
var rollbackRefPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._/\-]{0,127}$`)

// Rollback 把当前分支硬回退到指定提交或 tag；工作区有未提交修改时拒绝执行。
func (u *GitUpdater) Rollback(ctx context.Context, ref string) (Result, error) {
	ref = strings.TrimSpace(ref)
	if !rollbackRefPattern.MatchString(ref) {
		return Result{}, fmt.Errorf("updater: invalid rollback ref %q", ref)
	}
	status, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if status.Dirty {
		return Result{}, errors.New("updater: 工作区有未提交修改，回退会丢失这些改动，已拒绝执行")
	}
	// 先确认目标真实存在且是提交对象，再执行 reset。
	if _, err := u.gitOutput(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return Result{}, fmt.Errorf("updater: rollback target %q not found: %w", ref, err)
	}
	out, err := u.gitCombined(ctx, "reset", "--hard", ref)
	if err != nil {
		return Result{}, err
	}
	u.lastUpdateAt = time.Now()
	nextStatus, err := u.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	nextStatus.LastUpdateAt = u.lastUpdateAt
	result := Result{
		Status:  nextStatus,
		Updated: true,
		Output:  strings.TrimSpace(out),
		At:      time.Now(),
	}
	nextStatus.LastUpdateText = result.Output
	result.Status = nextStatus
	return result, nil
}

// gitOutput 执行 Git 命令并返回去空白输出。
func (u *GitUpdater) gitOutput(ctx context.Context, args ...string) (string, error) {
	out, err := u.gitCombined(ctx, args...)
	return strings.TrimSpace(out), err
}

// gitCombined 执行 Git 命令并合并 stdout/stderr。
func (u *GitUpdater) gitCombined(ctx context.Context, args ...string) (string, error) {
	// 所有 Git 命令都绑定仓库根目录运行，stdout/stderr 合并后返回给前端展示。
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = u.root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(out.String())
		if text == "" {
			return "", err
		}
		// 用 %w 保留底层退出错误，这样状态查询可以继续把“无远端/无上游”当成可忽略分支处理。
		return text, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, text)
	}
	return out.String(), nil
}
