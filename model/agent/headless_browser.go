// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"

	"golang.org/x/net/html"
)

const (
	defaultHeadlessBrowserTimeout     = 25 * time.Second
	defaultHeadlessBrowserHTMLBytes   = 4 * 1024 * 1024
	defaultHeadlessBrowserTextChars   = 8000
	defaultHeadlessBrowserVirtualTime = 8 * time.Second
	// headlessBrowserProbeTimeout 只够跑一次 --version；探测卡住不该拖住整个设置页。
	headlessBrowserProbeTimeout = 5 * time.Second
)

// RenderedPage is the sanitized result of one disposable headless browser run.
type RenderedPage struct {
	RequestedURL    string                 `json:"requested_url"`
	URL             string                 `json:"url"`
	Title           string                 `json:"title,omitempty"`
	Description     string                 `json:"description,omitempty"`
	Text            string                 `json:"text,omitempty"`
	Truncated       bool                   `json:"truncated,omitempty"`
	Sandboxed       bool                   `json:"sandboxed"`
	BrowserEngine   string                 `json:"browser_engine"`
	ReadyState      string                 `json:"ready_state,omitempty"`
	Stable          bool                   `json:"stable"`
	StabilityReason string                 `json:"stability_reason,omitempty"`
	WaitedMS        int64                  `json:"waited_ms,omitempty"`
	DOMChanges      int64                  `json:"dom_changes,omitempty"`
	ContentChanges  int64                  `json:"content_changes,omitempty"`
	PendingRequests int                    `json:"pending_requests,omitempty"`
	NavigationChain []string               `json:"navigation_chain,omitempty"`
	PreviousPages   []RenderedPageSnapshot `json:"previous_pages,omitempty"`
}

// RenderedPageSnapshot preserves meaningful content seen before a redirect.
type RenderedPageSnapshot struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Text        string `json:"text,omitempty"`
}

// PageRenderer renders one public HTTP(S) page without using a personal browser profile.
type PageRenderer interface {
	Render(ctx context.Context, rawURL string) (RenderedPage, error)
}

// PageRendererFunc adapts a function to PageRenderer.
type PageRendererFunc func(context.Context, string) (RenderedPage, error)

func (f PageRendererFunc) Render(ctx context.Context, rawURL string) (RenderedPage, error) {
	return f(ctx, rawURL)
}

type SandboxedBrowserConfig struct {
	Executable        string
	Timeout           time.Duration
	MaxHTMLBytes      int
	MaxTextChars      int
	VirtualTimeBudget time.Duration
	StabilityWindow   time.Duration
	NetworkIdleWindow time.Duration
	PollInterval      time.Duration
}

// SandboxedHeadlessBrowser starts a fresh Chrome/Chromium profile for every render.
type SandboxedHeadlessBrowser struct {
	cfg SandboxedBrowserConfig
}

func NewSandboxedHeadlessBrowser(cfg SandboxedBrowserConfig) *SandboxedHeadlessBrowser {
	return &SandboxedHeadlessBrowser{cfg: sandboxedBrowserConfigWithDefaults(cfg)}
}

func (b *SandboxedHeadlessBrowser) Render(ctx context.Context, rawURL string) (RenderedPage, error) {
	if b == nil {
		return RenderedPage{}, errors.New("headless browser is not configured")
	}
	if err := validateSandboxedBrowserURL(ctx, rawURL); err != nil {
		return RenderedPage{}, err
	}
	executable, err := findHeadlessBrowserExecutable(b.cfg.Executable)
	if err != nil {
		return RenderedPage{}, err
	}

	dirs, err := newBrowserSandboxDirs("diana-headless-browser-")
	if err != nil {
		return RenderedPage{}, err
	}
	defer dirs.remove()

	return b.renderObservable(ctx, executable, dirs.root, dirs.profile, dirs.cache, dirs.crash, rawURL)
}

func sandboxedBrowserConfigWithDefaults(cfg SandboxedBrowserConfig) SandboxedBrowserConfig {
	if cfg.Timeout <= 0 {
		cfg.Timeout = durationFromMillisecondsEnv("DIANA_HEADLESS_BROWSER_TIMEOUT_MS", defaultHeadlessBrowserTimeout)
	}
	if cfg.Timeout > MaxAllowedBrowserTimeoutMS*time.Millisecond {
		cfg.Timeout = MaxAllowedBrowserTimeoutMS * time.Millisecond
	}
	if cfg.MaxHTMLBytes <= 0 {
		cfg.MaxHTMLBytes = defaultHeadlessBrowserHTMLBytes
	}
	if cfg.MaxTextChars <= 0 {
		cfg.MaxTextChars = intFromEnv("DIANA_HEADLESS_BROWSER_MAX_CHARS", defaultHeadlessBrowserTextChars)
	}
	if cfg.MaxTextChars > MaxAllowedToolOutputChars {
		cfg.MaxTextChars = MaxAllowedToolOutputChars
	}
	if cfg.VirtualTimeBudget <= 0 {
		cfg.VirtualTimeBudget = durationFromMillisecondsEnv("DIANA_HEADLESS_BROWSER_VIRTUAL_TIME_MS", defaultHeadlessBrowserVirtualTime)
	}
	if cfg.VirtualTimeBudget > 15*time.Second {
		cfg.VirtualTimeBudget = 15 * time.Second
	}
	if cfg.StabilityWindow <= 0 {
		cfg.StabilityWindow = durationFromMillisecondsEnv("DIANA_HEADLESS_BROWSER_STABILITY_MS", 900*time.Millisecond)
	}
	if cfg.StabilityWindow > 5*time.Second {
		cfg.StabilityWindow = 5 * time.Second
	}
	if cfg.NetworkIdleWindow <= 0 {
		cfg.NetworkIdleWindow = durationFromMillisecondsEnv("DIANA_HEADLESS_BROWSER_NETWORK_IDLE_MS", 750*time.Millisecond)
	}
	if cfg.NetworkIdleWindow > 5*time.Second {
		cfg.NetworkIdleWindow = 5 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = durationFromMillisecondsEnv("DIANA_HEADLESS_BROWSER_POLL_MS", 250*time.Millisecond)
	}
	if cfg.PollInterval < 100*time.Millisecond {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.PollInterval > time.Second {
		cfg.PollInterval = time.Second
	}
	return cfg
}

func durationFromMillisecondsEnv(key string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func intFromEnv(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// HeadlessBrowserStatus 是沙盒无头浏览器的可用性探测结果。
type HeadlessBrowserStatus struct {
	Available bool
	Path      string
	Version   string
	// Detail 在不可用时说明卡在哪一步，直接给用户看。
	Detail string
}

// ProbeHeadlessBrowser 检查沙盒渲染现在能不能真的跑起来。
//
// 判定以「--version 真的执行成功」为准，而不是文件存在：架构不匹配、缺动态库、
// 悬空的符号链接都能骗过存在性检查，却一渲染就失败——那时候用户已经在群里发过
// 链接了，只会看到一句「渲染失败」，还不知道是环境问题。
func ProbeHeadlessBrowser(ctx context.Context, configured string) HeadlessBrowserStatus {
	path, err := findHeadlessBrowserExecutable(configured)
	if err != nil {
		return HeadlessBrowserStatus{Detail: headlessBrowserProbeDetail(configured)}
	}
	// Windows 上的 chrome.exe 是 GUI 子系统程序，--version 什么都不往标准输出写，
	// 拿它判活会把装好的 Chrome 判成不可用。这里退回到「可执行文件存在」，
	// 版本号留空。
	if runtime.GOOS == "windows" {
		return HeadlessBrowserStatus{Available: true, Path: path}
	}
	probeCtx, cancel := context.WithTimeout(ctx, headlessBrowserProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		detail := "找到了 " + path + "，但它执行失败"
		if message := compactBrowserError(string(output)); message != "" {
			detail += "：" + message
		}
		return HeadlessBrowserStatus{Path: path, Detail: detail}
	}
	version := strings.TrimSpace(firstNonEmptyLine(string(output)))
	return HeadlessBrowserStatus{Available: true, Path: path, Version: version}
}

// ProbeHeadlessBrowserRendering 在版本探测之后再完成一次真实的本地截图。
// --version 只能证明文件能执行，不能发现浏览器启动后卡住、沙箱参数不兼容或无法
// 产出 PNG。插件依赖页用这个探测，结论才和真正调用时一致。
func ProbeHeadlessBrowserRendering(ctx context.Context, configured string) HeadlessBrowserStatus {
	status := ProbeHeadlessBrowser(ctx, configured)
	if !status.Available {
		return status
	}
	probeCtx, cancel := context.WithTimeout(ctx, headlessBrowserProbeTimeout)
	defer cancel()
	_, err := CaptureHTMLScreenshot(probeCtx, ScreenshotRequest{
		HTML:       `<!doctype html><meta charset="utf-8"><title>Diana browser probe</title><body>ok</body>`,
		Width:      64,
		Height:     64,
		Executable: status.Path,
		Timeout:    headlessBrowserProbeTimeout,
	})
	if err != nil {
		status.Available = false
		status.Detail = "找到了 " + status.Path + "，但真实截图失败：" + err.Error()
	}
	return status
}

func headlessBrowserProbeDetail(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return "配置的浏览器路径不存在：" + strings.TrimSpace(configured)
	}
	return "没有找到 Chrome/Chromium"
}

func firstNonEmptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// findHeadlessBrowserExecutable 解析浏览器可执行文件。三条路径共用它：网页渲染、
// 截图和可用性探测。
//
// 环境变量的兜底必须在这里，不能只写在 SandboxedBrowserConfig 的默认值里——那样
// 只有网页渲染认得它，截图和探测走的是另一条入口。浏览器装在非标准路径、靠环境
// 变量指过去的机器上，症状就是网页渲染能用，关系图却报「这台机器上没有浏览器」。
func findHeadlessBrowserExecutable(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = firstNonEmptyString(
			os.Getenv("DIANA_HEADLESS_BROWSER_EXECUTABLE"),
			os.Getenv("DIANA_AGENT_BROWSER_EXECUTABLE"),
		)
	}
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		if path, err := exec.LookPath(configured); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("configured headless browser executable not found: %s", configured)
	}

	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		)
	case "linux":
		// systemd 起的服务通常只继承 /usr/bin:/bin:/usr/sbin:/sbin，snap 和 /opt
		// 下装的浏览器靠 PATH 一个都找不到，界面上就成了「没装浏览器」。
		paths = append(paths,
			"/opt/google/chrome/chrome",
			"/snap/bin/chromium",
			"/usr/lib/chromium/chromium",
			"/usr/lib/chromium-browser/chromium-browser",
		)
	case "windows":
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			)
		}
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("Chrome/Chromium executable was not found")
}

// sandboxedChromeBaseArgs 是两条渲染路径共用的沙盒加固参数：CDP 那条（网页渲染）
// 和一次性截图那条（关系图）。
//
// 抽出来之前截图自己手抄了一份子集，抄漏的不只是重复代码——它没有
// --host-resolver-rules，也就是说那条路没有把 localhost 和内网域名挡掉，加固强度
// 和网页渲染不一样。同一个进程里跑的两个浏览器，沙盒不该有强弱之分。
//
// 只放两条路都成立的参数：带模式色彩的（远程调试端口、截图输出、窗口尺寸）留给
// 各自的调用点追加。
// browserSandboxDirs 是一次浏览器运行的临时目录组：整棵树只归这次运行，用完删掉。
// 两条渲染路径都要它，别再各写一遍——目录权限（0700）和清理时机这种事，写错一次
// 就是一个留在 /tmp 里的可读 profile。
type browserSandboxDirs struct {
	root    string
	profile string
	cache   string
	crash   string
}

func newBrowserSandboxDirs(prefix string) (browserSandboxDirs, error) {
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		return browserSandboxDirs{}, err
	}
	dirs := browserSandboxDirs{
		root:    root,
		profile: filepath.Join(root, "profile"),
		cache:   filepath.Join(root, "cache"),
		crash:   filepath.Join(root, "crash"),
	}
	if err := os.Chmod(root, 0o700); err != nil {
		dirs.remove()
		return browserSandboxDirs{}, err
	}
	for _, dir := range []string{dirs.profile, dirs.cache, dirs.crash} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dirs.remove()
			return browserSandboxDirs{}, err
		}
	}
	return dirs, nil
}

func (d browserSandboxDirs) remove() {
	if d.root != "" {
		_ = os.RemoveAll(d.root)
	}
}

func sandboxedChromeBaseArgs(profileDir, cacheDir, crashDir string) []string {
	return []string{
		"--headless=new",
		"--user-data-dir=" + profileDir,
		"--disk-cache-dir=" + cacheDir,
		"--crash-dumps-dir=" + crashDir,
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-features=AutofillServerCommunication,MediaRouter,OptimizationHints,Translate",
		"--disable-notifications",
		"--disable-sync",
		"--metrics-recording-only",
		"--mute-audio",
		"--no-default-browser-check",
		"--no-first-run",
		"--no-pings",
		"--password-store=basic",
		"--use-mock-keychain",
		"--hide-scrollbars",
		"--host-resolver-rules=MAP localhost ~NOTFOUND, MAP *.localhost ~NOTFOUND, MAP *.local ~NOTFOUND, MAP host.docker.internal ~NOTFOUND, MAP gateway.docker.internal ~NOTFOUND",
	}
}

func sandboxedChromeArgs(profileDir, cacheDir, crashDir string, cfg SandboxedBrowserConfig) []string {
	return append(sandboxedChromeBaseArgs(profileDir, cacheDir, crashDir),
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--window-size=1280,960",
	)
}

func sandboxedBrowserEnvironment(current []string, root string) []string {
	blocked := map[string]bool{
		"HOME": true, "TMPDIR": true, "XDG_CACHE_HOME": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	}
	out := make([]string, 0, len(current)+5)
	for _, value := range current {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			out = append(out, value)
		}
	}
	return append(out,
		"HOME="+root,
		"TMPDIR="+root,
		"XDG_CACHE_HOME="+filepath.Join(root, "xdg-cache"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(root, "xdg-data"),
	)
}

func validateSandboxedBrowserURL(ctx context.Context, value string) error {
	if err := validateBrowserURL(value); err != nil {
		return err
	}
	return netguard.ValidatePublicURLStrict(ctx, value)
}

func parseRenderedPage(data []byte, requestedURL string, maxChars int, truncated bool) (RenderedPage, error) {
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return RenderedPage{}, fmt.Errorf("parse rendered page: %w", err)
	}
	page := RenderedPage{
		RequestedURL: requestedURL,
		URL:          requestedURL,
		Truncated:    truncated,
	}
	page.Title = truncateText(normalizeRenderedText(nodeText(findElement(document, "title"))), 300)
	page.Description = truncateText(firstNonEmptyString(
		metaContent(document, "name", "description"),
		metaContent(document, "property", "og:description"),
	), 800)
	if canonical := firstNonEmptyString(
		linkHref(document, "canonical"),
		metaContent(document, "property", "og:url"),
	); canonical != "" {
		if resolved := resolveRenderedPageURL(requestedURL, canonical); resolved != "" {
			page.URL = resolved
		}
	}
	content := firstNonNilNode(findElement(document, "main"), findElement(document, "article"), findElement(document, "body"), document)
	page.Text = truncateText(normalizeRenderedText(visibleNodeText(content)), maxChars)
	if maxChars > 0 && len([]rune(normalizeRenderedText(visibleNodeText(content)))) > maxChars {
		page.Truncated = true
	}
	if page.Title == "" && page.Description == "" && page.Text == "" {
		return RenderedPage{}, errors.New("headless browser rendered an empty page")
	}
	return page, nil
}

func findElement(node *html.Node, tag string) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, tag) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func firstNonNilNode(nodes ...*html.Node) *html.Node {
	for _, node := range nodes {
		if node != nil {
			return node
		}
	}
	return nil
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func visibleNodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node, bool)
	walk = func(current *html.Node, hidden bool) {
		if current == nil {
			return
		}
		if current.Type == html.ElementNode {
			tag := strings.ToLower(current.Data)
			switch tag {
			case "script", "style", "noscript", "svg", "canvas", "template", "head":
				return
			}
			hidden = hidden || nodeIsHidden(current)
			if hidden {
				return
			}
			if renderedBlockTags[tag] {
				builder.WriteByte('\n')
			}
		}
		if current.Type == html.TextNode && !hidden {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child, hidden)
		}
		if current.Type == html.ElementNode && renderedBlockTags[strings.ToLower(current.Data)] {
			builder.WriteByte('\n')
		}
	}
	walk(node, false)
	return builder.String()
}

var renderedBlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "dd": true, "div": true, "dl": true, "dt": true,
	"fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hr": true, "li": true,
	"main": true, "nav": true, "ol": true, "p": true, "pre": true,
	"section": true, "table": true, "tbody": true, "td": true,
	"tfoot": true, "th": true, "thead": true, "tr": true, "ul": true,
}

func nodeIsHidden(node *html.Node) bool {
	for _, attr := range node.Attr {
		key := strings.ToLower(attr.Key)
		value := strings.ToLower(strings.TrimSpace(attr.Val))
		switch key {
		case "hidden":
			return true
		case "aria-hidden":
			return value == "true"
		case "style":
			compact := strings.ReplaceAll(value, " ", "")
			return strings.Contains(compact, "display:none") || strings.Contains(compact, "visibility:hidden")
		}
	}
	return false
}

func metaContent(document *html.Node, attrName, attrValue string) string {
	var result string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || result != "" {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "meta") && strings.EqualFold(nodeAttr(node, attrName), attrValue) {
			result = normalizeRenderedText(nodeAttr(node, "content"))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return result
}

func linkHref(document *html.Node, rel string) string {
	var result string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || result != "" {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "link") {
			for _, value := range strings.Fields(strings.ToLower(nodeAttr(node, "rel"))) {
				if value == strings.ToLower(rel) {
					result = strings.TrimSpace(nodeAttr(node, "href"))
					return
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return result
}

func nodeAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func resolveRenderedPageURL(base, candidate string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	candidateURL, err := url.Parse(candidate)
	if err != nil {
		return ""
	}
	resolved := baseURL.ResolveReference(candidateURL)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

func normalizeRenderedText(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	last := ""
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || line == last {
			continue
		}
		out = append(out, line)
		last = line
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "\n...[truncated]"
}

func compactBrowserError(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return truncateText(strings.Join(lines, " "), 600)
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
			b.truncated = true
		}
		_, _ = b.buffer.Write(data)
	} else if originalLength > 0 {
		b.truncated = true
	}
	return originalLength, nil
}

func (b *cappedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *cappedBuffer) String() string { return b.buffer.String() }
func (b *cappedBuffer) Len() int       { return b.buffer.Len() }

type BrowserRenderTool struct {
	renderer PageRenderer
}

func NewBrowserRenderTool(renderer PageRenderer) *BrowserRenderTool {
	if renderer == nil {
		renderer = NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{})
	}
	return &BrowserRenderTool{renderer: renderer}
}

func (t *BrowserRenderTool) Name() string { return "browser_render" }

func (t *BrowserRenderTool) Description() string {
	return `在一次性隔离配置的无头 Chrome/Chromium 中渲染公网网页并读取最终 DOM 文本，不使用用户浏览器登录态。`
}

func (t *BrowserRenderTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"url"}, map[string]any{
		"url": toolStringParam("要渲染的公网页面地址"),
	})
}

func (t *BrowserRenderTool) Run(ctx context.Context, input map[string]any) (string, error) {
	rawURL := stringFromInput(input, "url")
	page, err := t.renderer.Render(ctx, rawURL)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
