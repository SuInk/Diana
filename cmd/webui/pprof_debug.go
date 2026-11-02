// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
)

// startPprofFromEnv 在设置了 DIANA_PPROF_ADDR 时起一个只给本机看的 pprof 端口，
// 用来量内存和 CPU。默认不开；地址必须是回环，免得把堆里的密钥暴露到网上。
func startPprofFromEnv() {
	addr := strings.TrimSpace(os.Getenv("DIANA_PPROF_ADDR"))
	if addr == "" {
		return
	}
	if !loopbackListenAddr(addr) {
		log.Printf("DIANA_PPROF_ADDR=%q ignored: must be a loopback host:port such as 127.0.0.1:6060", addr)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		defer recoverGoroutinePanic("pprof")
		log.Printf("pprof listening on http://%s/debug/pprof/", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("pprof listener stopped: %v", err)
		}
	}()
}

// loopbackListenAddr 只认「回环 IP:端口」。主机名不认：localhost 能被 hosts 文件改指。
func loopbackListenAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
