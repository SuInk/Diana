// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/netguard"
)

// A proxy applies the allowlist to redirects, popups and background requests,
// including those happening between tool calls. It never uses process proxies.
type userBrowserProxy struct {
	url         string
	hosts       []string
	server      *http.Server
	transport   *http.Transport
	mu          sync.Mutex
	connections map[net.Conn]bool
	closed      bool
}

func newUserBrowserProxy(hosts []string) (*userBrowserProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &userBrowserProxy{url: "http://" + listener.Addr().String(), hosts: agent.NormalizeBrowserScreenshotHosts(hosts), connections: map[net.Conn]bool{}}
	p.transport = http.DefaultTransport.(*http.Transport).Clone()
	p.transport.Proxy = nil
	p.transport.DialContext = netguard.DialPublicContextStrict
	p.transport.ResponseHeaderTimeout = 20 * time.Second
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() { defer recoverGoroutinePanic("userBrowserProxy"); _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *userBrowserProxy) allowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	value := strings.ToLower(parsed.Host)
	defaultPort := "80"
	if parsed.Scheme == "https" {
		defaultPort = "443"
	}
	for _, host := range p.hosts {
		if value == host || (parsed.Port() == defaultPort && strings.ToLower(parsed.Hostname()) == host) || (parsed.Port() == "" && value+":"+defaultPort == host) {
			return true
		}
	}
	return false
}

func (p *userBrowserProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL == nil || r.URL.Scheme != "http" || !p.allowed(r.URL.String()) || netguard.ValidatePublicURLStrict(r.Context(), r.URL.String()) != nil {
		http.Error(w, "网站不在个人浏览器白名单内或不是公网地址", http.StatusForbidden)
		return
	}
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.Host = r.URL.Host
	stripProxyHopHeaders(request.Header)
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "个人浏览器无法连接网站", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	stripProxyHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (p *userBrowserProxy) connect(w http.ResponseWriter, r *http.Request) {
	if !p.allowed("https://"+r.Host) || netguard.ValidatePublicURLStrict(r.Context(), "https://"+r.Host) != nil {
		http.Error(w, "登录跳转的网站不在个人浏览器白名单内或不是公网地址", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	remote, err := netguard.DialPublicContextStrict(ctx, "tcp", r.Host)
	if err != nil {
		http.Error(w, "个人浏览器无法连接网站", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		remote.Close()
		http.Error(w, "proxy unavailable", http.StatusInternalServerError)
		return
	}
	local, buffer, err := hijacker.Hijack()
	if err != nil {
		remote.Close()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		local.Close()
		remote.Close()
		return
	}
	p.connections[local], p.connections[remote] = true, true
	p.mu.Unlock()
	defer func() {
		local.Close()
		remote.Close()
		p.mu.Lock()
		delete(p.connections, local)
		delete(p.connections, remote)
		p.mu.Unlock()
	}()
	_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("userProxyTunnel")
		defer close(done)
		_, _ = io.Copy(remote, buffer)
		remote.Close()
	}()
	_, _ = io.Copy(local, remote)
	local.Close()
	<-done
}

func (p *userBrowserProxy) Close() {
	p.mu.Lock()
	p.closed = true
	for connection := range p.connections {
		connection.Close()
	}
	p.mu.Unlock()
	p.transport.CloseIdleConnections()
	_ = p.server.Close()
}

func stripProxyHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
