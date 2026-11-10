// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const allowPrivateFetchesEnv = "DIANA_ALLOW_PRIVATE_HTTP_FETCHES"

// publicHostLookupTimeout 是放行前那次域名解析的时限。以前是 3 秒，比系统解析器
// 重试一次还短：标准解析器（glibc、Go 自带的都是）单次等 5 秒、共试 2 次，Go 默认
// Transport 给解析加连接留 30 秒。第一个 DNS 没应答、换下一个时正好 3 秒出头
// （2026-11-10 线上实测 3.12 秒），头像这类没缓存的域名就卡在 3.00 秒整失败。
// 现在留够两次尝试；调用方自己的总时限（图片下载是 8 秒）仍然管着上限。
const publicHostLookupTimeout = 10 * time.Second

// NewPublicHTTPClient returns a client that only connects to public HTTP(S)
// destinations and revalidates every redirect target.
func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Untrusted URL fetches intentionally ignore process HTTP proxy variables.
	// A local proxy would otherwise become an SSRF hop outside this guard.
	transport.Proxy = nil
	transport.DialContext = guardedDialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: &publicRoundTripper{base: transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return ValidatePublicURL(req.Context(), req.URL.String())
		},
	}
}

// 下载内容（图片、语音、视频、文件）不设总时限：文件大小和网速差得太远，一个固定的
// 总秒数要么掐断慢但正常的下载，要么对卡死的连接等得太久。和 curl、浏览器、Go 默认
// Transport 一样，只限每个阶段：连接 30 秒、等响应头 30 秒、正文连续 30 秒没有数据
// 才算卡死。调用方 ctx 取消仍然立刻生效，大小上限由调用方自己管。
var (
	publicDialTimeout             = 30 * time.Second
	downloadResponseHeaderTimeout = 30 * time.Second
	downloadStallTimeout          = 30 * time.Second
)

// ErrDownloadStalled 表示连接还在，但正文长时间没有新数据。
var ErrDownloadStalled = errors.New("download stalled: no data received")

// NewPublicDownloadClient 和 NewPublicHTTPClient 一样只连公网地址，但不设总时限，
// 改为限制各阶段，见上面的说明。用于下载内容；调接口、抓页面仍用带总时限的那个。
func NewPublicDownloadClient() *http.Client {
	client := NewPublicHTTPClient(0)
	guarded := client.Transport.(*publicRoundTripper)
	guarded.base.(*http.Transport).ResponseHeaderTimeout = downloadResponseHeaderTimeout
	guarded.stall = downloadStallTimeout
	return client
}

type publicRoundTripper struct {
	base http.RoundTripper
	// stall 大于 0 时，响应正文连续这么久没有数据就中断。
	stall time.Duration
}

func (t *publicRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("public HTTP request URL is required")
	}
	if err := ValidatePublicURL(req.Context(), req.URL.String()); err != nil {
		return nil, err
	}
	if t.stall <= 0 {
		return t.base.RoundTrip(req)
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	body := &stallGuardBody{ReadCloser: resp.Body, ctx: ctx, cancel: cancel, stall: t.stall}
	body.timer = time.AfterFunc(t.stall, func() { cancel(ErrDownloadStalled) })
	resp.Body = body
	return resp, nil
}

type stallGuardBody struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	stall  time.Duration
}

func (b *stallGuardBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.stall)
	}
	if err != nil && err != io.EOF && errors.Is(context.Cause(b.ctx), ErrDownloadStalled) {
		return n, fmt.Errorf("%w for %s", ErrDownloadStalled, b.stall)
	}
	return n, err
}

func (b *stallGuardBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}

// ValidatePublicURL rejects credentials and hosts that resolve to local,
// private, reserved, or non-unicast addresses.
func ValidatePublicURL(ctx context.Context, value string) error {
	return validatePublicURL(ctx, value, privateFetchesAllowed())
}

// ValidatePublicURLStrict always rejects private destinations, including when
// trusted media fetches were explicitly enabled for the process.
func ValidatePublicURLStrict(ctx context.Context, value string) error {
	return validatePublicURL(ctx, value, false)
}

func validatePublicURL(ctx context.Context, value string, allowPrivate bool) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http and https URLs are allowed")
	}
	if parsed.User != nil {
		return errors.New("URLs containing credentials are not allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return errors.New("URL host is required")
	}
	if allowPrivate {
		return nil
	}
	if blockedHostname(host) {
		return fmt.Errorf("access to local host %q is blocked", host)
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if unsafeAddress(addr, false) {
			return fmt.Errorf("access to private or reserved address %q is blocked", host)
		}
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(contextOrBackground(ctx), publicHostLookupTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return fmt.Errorf("host resolution failed: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("host did not resolve to an address")
	}
	for _, address := range addresses {
		if unsafeAddress(address.Unmap(), true) {
			return fmt.Errorf("host %q resolves to a private or reserved address", host)
		}
	}
	return nil
}

func guardedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if privateFetchesAllowed() {
		return (&net.Dialer{Timeout: publicDialTimeout}).DialContext(ctx, network, address)
	}
	return DialPublicContextStrict(ctx, network, address)
}

// DialPublicContextStrict pins a validated public IP and ignores media-fetch opt-ins.
func DialPublicContextStrict(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")
	if blockedHostname(strings.ToLower(strings.TrimSuffix(host, "."))) {
		return nil, fmt.Errorf("access to local host %q is blocked", host)
	}
	addresses, err := net.DefaultResolver.LookupNetIP(contextOrBackground(ctx), "ip", host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, address := range addresses {
		address = address.Unmap()
		if unsafeAddress(address, net.ParseIP(host) == nil) {
			continue
		}
		conn, dialErr := (&net.Dialer{Timeout: publicDialTimeout}).DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("host %q has no allowed address", host)
}

func blockedHostname(host string) bool {
	if host == "localhost" || host == "localhost.localdomain" || host == "metadata.google.internal" {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return host == "host.docker.internal" || host == "gateway.docker.internal"
}

func unsafeAddress(addr netip.Addr, allowSyntheticDNS bool) bool {
	addr = addr.Unmap()
	if allowSyntheticDNS && netip.MustParsePrefix("198.18.0.0/15").Contains(addr) {
		// Transparent proxy/TUN clients on macOS commonly use RFC 2544 fake IPs.
		return false
	}
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func privateFetchesAllowed() bool {
	allowed, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(allowPrivateFetchesEnv)))
	return err == nil && allowed
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
