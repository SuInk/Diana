// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package termux

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ConfigureDNS 让 Go 的解析器用 Termux 的 DNS 配置。
//
// 发布包是 CGO_ENABLED=0 的纯 Go 程序，解析器只认 /etc/resolv.conf；安卓没有这个文件，
// Go 就退回去问 [::1]:53，手机上没人听，所有域名都解析失败——模型、网页读取、
// 机器人连接一个都通不了。Termux 把配置放在 $PREFIX/etc/resolv.conf，这里把
// net.DefaultResolver 指过去。不在 Termux 里，或者 /etc/resolv.conf 本来就有，什么都不改。
//
// 只能在启动时、还没有任何请求之前调用一次。
func ConfigureDNS() bool {
	prefix := Prefix()
	if prefix == "" {
		return false
	}
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return false
	}
	servers := readNameservers(filepath.Join(prefix, "etc", "resolv.conf"))
	if len(servers) == 0 {
		return false
	}
	var dialer net.Dialer
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var errs []error
		for _, server := range servers {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(server, "53"))
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}
	return true
}

// readNameservers 读 resolv.conf 里的 nameserver 行，读不到就是空。
func readNameservers(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var servers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" && net.ParseIP(fields[1]) != nil {
			servers = append(servers, fields[1])
		}
	}
	return servers
}
