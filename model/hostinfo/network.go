// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package hostinfo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// AddressScope 是一个 IP 的可达范围。
type AddressScope string

const (
	ScopeLoopback  AddressScope = "loopback"
	ScopeLAN       AddressScope = "lan"
	ScopePublic    AddressScope = "public"
	ScopeLinkLocal AddressScope = "link_local"
)

// InterfaceAddress 是本机某块网卡上的一个地址。
type InterfaceAddress struct {
	Interface string       `json:"interface"`
	IP        string       `json:"ip"`
	Family    string       `json:"family"`
	Scope     AddressScope `json:"scope"`
	URL       string       `json:"url,omitempty"`
}

// PublicAddress 是从外部服务看到的出口地址。
type PublicAddress struct {
	IP      string    `json:"ip,omitempty"`
	Source  string    `json:"source,omitempty"`
	Error   string    `json:"error,omitempty"`
	Checked time.Time `json:"checked_at"`
	URL     string    `json:"url,omitempty"`
}

// NetworkSnapshot 汇总监听端口、网卡地址和（按需探测的）公网出口地址。
type NetworkSnapshot struct {
	ListenHost string             `json:"listen_host"`
	Port       string             `json:"port"`
	Hostname   string             `json:"hostname"`
	Addresses  []InterfaceAddress `json:"addresses"`
	// Public 只在调用方要求时才探测：要向外部服务发请求，不该每次打开页面都发。
	Public *PublicAddress `json:"public,omitempty"`
	// Container 为 true 时网卡地址是容器内部的，宿主机局域网地址要看端口映射。
	Container bool `json:"container"`
}

// ClassifyIP 判断一个地址属于本机、局域网、链路本地还是公网。
// 100.64.0.0/10（运营商级 NAT、Tailscale）不可从公网直达，归为局域网。
func ClassifyIP(ip net.IP) AddressScope {
	switch {
	case ip.IsLoopback():
		return ScopeLoopback
	case ip.IsLinkLocalUnicast():
		return ScopeLinkLocal
	case ip.IsPrivate() || cgnat.Contains(ip):
		return ScopeLAN
	default:
		return ScopePublic
	}
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// LocalAddresses 列出本机启用网卡上的地址，按回环、局域网、公网、链路本地排序。
// listenHost 非空且不是通配地址时，只有它会被监听，URL 只给它填。
func LocalAddresses(listenHost, port string) ([]InterfaceAddress, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []InterfaceAddress
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP
			family := "ipv6"
			if ip.To4() != nil {
				family = "ipv4"
			}
			entry := InterfaceAddress{Interface: iface.Name, IP: ip.String(), Family: family, Scope: ClassifyIP(ip)}
			if entry.Scope != ScopeLinkLocal && listensOn(listenHost, ip) {
				entry.URL = httpURL(ip.String(), port)
			}
			out = append(out, entry)
		}
	}
	rank := map[AddressScope]int{ScopeLoopback: 0, ScopeLAN: 1, ScopePublic: 2, ScopeLinkLocal: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Scope] != rank[out[j].Scope] {
			return rank[out[i].Scope] < rank[out[j].Scope]
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

// listensOn 判断监听地址是否覆盖 ip。空、0.0.0.0、:: 都是通配。
func listensOn(listenHost string, ip net.IP) bool {
	host := strings.Trim(strings.TrimSpace(listenHost), "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	if host == "localhost" {
		return ip.IsLoopback()
	}
	listen := net.ParseIP(host)
	return listen != nil && listen.Equal(ip)
}

func httpURL(host, port string) string {
	if strings.TrimSpace(port) == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

// publicIPServices 按顺序尝试，返回纯文本 IP。多备一家，单家被墙或限流时还能查到。
var publicIPServices = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

// publicIPCacheTTL 内重复探测直接用上次结果，页面反复刷新不会一直打外部服务。
const publicIPCacheTTL = 5 * time.Minute

var publicIPCache struct {
	sync.Mutex
	value PublicAddress
}

// DetectPublicIP 查询本机的公网出口地址。force 为 false 时命中缓存直接返回。
func DetectPublicIP(ctx context.Context, client *http.Client, force bool) PublicAddress {
	publicIPCache.Lock()
	defer publicIPCache.Unlock()
	if !force && publicIPCache.value.IP != "" && time.Since(publicIPCache.value.Checked) < publicIPCacheTTL {
		return publicIPCache.value
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	result := PublicAddress{Checked: time.Now()}
	var errs []string
	for _, service := range publicIPServices {
		ip, err := fetchPublicIP(ctx, client, service)
		if err == nil {
			result.IP, result.Source = ip, service
			break
		}
		errs = append(errs, err.Error())
	}
	if result.IP == "" {
		result.Error = "公网地址探测失败：" + strings.Join(errs, "；")
	}
	publicIPCache.value = result
	return result
}

func fetchPublicIP(ctx context.Context, client *http.Client, service string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, service, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(service + " 返回 " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return "", errors.New(service + " 返回的不是 IP")
	}
	return ip.String(), nil
}

// CollectNetwork 组装网络快照。withPublic 为 true 才会向外部服务查公网地址；
// 公网 URL 只是「端口映射或防火墙放行后」的访问地址，不代表外网一定能连上。
func CollectNetwork(ctx context.Context, listenHost, port string, withPublic, force bool, container bool) (NetworkSnapshot, error) {
	addrs, err := LocalAddresses(listenHost, port)
	if err != nil {
		return NetworkSnapshot{}, err
	}
	hostname, _ := os.Hostname()
	snap := NetworkSnapshot{ListenHost: listenHost, Port: port, Hostname: hostname, Addresses: addrs, Container: container}
	if withPublic {
		public := DetectPublicIP(ctx, nil, force)
		if public.IP != "" {
			public.URL = httpURL(public.IP, port)
		}
		snap.Public = &public
	}
	return snap, nil
}
