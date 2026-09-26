// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package osc

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/SuInk/diana/internal/safego"
)

// maxDatagram 是 UDP 单包载荷上限。OSC over UDP 一个包就是一条消息或一个 bundle。
const maxDatagram = 65507

// Client 把消息发往固定的 UDP 目标。并发安全。
type Client struct {
	conn *net.UDPConn
}

// Dial 解析地址并建立 UDP「连接」。UDP 没有握手，这里只是绑定目标地址，
// 对端没在听也不会报错——VRChat 没开时发出去的包就静静丢了。
func Dial(address string) (*Client, error) {
	remote, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("osc: resolve %s: %w", address, err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return nil, fmt.Errorf("osc: dial %s: %w", address, err)
	}
	return &Client{conn: conn}, nil
}

// Send 编码并发送一条消息。
func (c *Client) Send(message Message) error {
	data, err := message.MarshalBinary()
	if err != nil {
		return err
	}
	return c.write(data)
}

// SendBundle 把几条消息打进一个 bundle 一次发出，接收端会原子地处理它们。
func (c *Client) SendBundle(bundle Bundle) error {
	data, err := bundle.MarshalBinary()
	if err != nil {
		return err
	}
	return c.write(data)
}

func (c *Client) write(data []byte) error {
	if c == nil || c.conn == nil {
		return errors.New("osc: client is closed")
	}
	if len(data) > maxDatagram {
		return fmt.Errorf("osc: packet of %d bytes exceeds UDP limit", len(data))
	}
	_, err := c.conn.Write(data)
	return err
}

// RemoteAddr 返回发送目标。
func (c *Client) RemoteAddr() string {
	if c == nil || c.conn == nil {
		return ""
	}
	return c.conn.RemoteAddr().String()
}

// Close 释放套接字。
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Handler 处理收到的一条消息。bundle 会被拆开逐条交给它。
type Handler func(Message, *net.UDPAddr)

// Server 监听一个 UDP 端口并把收到的 OSC 消息交给 Handler。
type Server struct {
	conn    *net.UDPConn
	handler Handler
	done    chan struct{}
	once    sync.Once
}

// Listen 绑定地址并在后台开始收包。端口写 0 时由系统分配，测试靠它避开端口冲突。
func Listen(address string, handler Handler) (*Server, error) {
	local, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("osc: resolve %s: %w", address, err)
	}
	conn, err := net.ListenUDP("udp", local)
	if err != nil {
		return nil, fmt.Errorf("osc: listen %s: %w", address, err)
	}
	server := &Server{conn: conn, handler: handler, done: make(chan struct{})}
	go func() {
		defer recoverPanic("osc.server")
		server.serve()
	}()
	return server, nil
}

func (s *Server) serve() {
	defer close(s.done)
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// 单个坏包或 ICMP 回显不该让监听停掉。
			continue
		}
		// 解不开的包直接丢：局域网里谁都能往这个端口发东西。
		messages, err := ParsePacket(buf[:n])
		if err != nil || s.handler == nil {
			continue
		}
		for _, message := range messages {
			s.dispatch(message, from)
		}
	}
}

// dispatch 把 Handler 的 panic 挡在单条消息里，收包循环照常继续。
func (s *Server) dispatch(message Message, from *net.UDPAddr) {
	defer safego.Recover("osc.server")
	s.handler(message, from)
}

// LocalAddr 返回实际监听的地址。
func (s *Server) LocalAddr() *net.UDPAddr {
	if s == nil || s.conn == nil {
		return nil
	}
	addr, _ := s.conn.LocalAddr().(*net.UDPAddr)
	return addr
}

// Close 停止监听并等待收包协程退出，之后不会再调用 Handler。
func (s *Server) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		err = s.conn.Close()
		<-s.done
	})
	return err
}

// recoverPanic 必须自己调 recover()：交给 safego.Recover 再转一层就接不住了。
func recoverPanic(component string) {
	safego.Report(component, recover())
}
