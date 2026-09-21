// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Target 是浏览器里的一个标签页。只留实时画面和标签页列表用得上的字段。
type Target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

const cdpHTTPTimeout = 10 * time.Second

func cdpHTTP(ctx context.Context, method, endpoint string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: cdpHTTPTimeout}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("连不上内置浏览器：%w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("内置浏览器返回 %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

// ListTabs 列出可见的标签页。扩展页、devtools 这类非 page 目标不给出去。
func ListTabs(ctx context.Context, base string) ([]Target, error) {
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("内置浏览器没有运行")
	}
	var targets []Target
	if err := cdpHTTP(ctx, http.MethodGet, base+"/json/list", &targets); err != nil {
		return nil, err
	}
	pages := make([]Target, 0, len(targets))
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		pages = append(pages, target)
	}
	return pages, nil
}

// OpenTab 新开一个标签页。
func OpenTab(ctx context.Context, base, pageURL string) (Target, error) {
	var target Target
	endpoint := base + "/json/new?" + url.QueryEscape(pageURL)
	// 新版 Chrome 只接受 PUT，老版本只认 GET；先 PUT 再退回 GET。
	if err := cdpHTTP(ctx, http.MethodPut, endpoint, &target); err != nil {
		if fallbackErr := cdpHTTP(ctx, http.MethodGet, endpoint, &target); fallbackErr != nil {
			return Target{}, err
		}
	}
	return target, nil
}

// CloseTab 关掉一个标签页。
func CloseTab(ctx context.Context, base, id string) error {
	return cdpHTTP(ctx, http.MethodGet, base+"/json/close/"+url.PathEscape(id), nil)
}

// ActivateTab 把标签页切到前台。无头模式下它决定谁是「当前页」，实时画面跟着它走。
func ActivateTab(ctx context.Context, base, id string) error {
	return cdpHTTP(ctx, http.MethodGet, base+"/json/activate/"+url.PathEscape(id), nil)
}

// Session 是连到某个标签页的 CDP 会话。调用方负责 Close。
type Session struct {
	conn *websocket.Conn

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage

	events chan Event
	closed chan struct{}
	once   sync.Once
}

// Event 是 CDP 事件。
type Event struct {
	Method string
	Params json.RawMessage
}

// Dial 连上一个标签页的调试端点。
func Dial(ctx context.Context, websocketURL string) (*Session, error) {
	dialer := websocket.Dialer{HandshakeTimeout: cdpHTTPTimeout}
	conn, _, err := dialer.DialContext(ctx, websocketURL, nil)
	if err != nil {
		return nil, fmt.Errorf("连不上标签页：%w", err)
	}
	// 一帧 JPEG 可能有几百 KB，默认读上限会把画面直接截断。
	conn.SetReadLimit(32 << 20)
	session := &Session{
		conn:    conn,
		pending: map[int]chan json.RawMessage{},
		events:  make(chan Event, 64),
		closed:  make(chan struct{}),
	}
	go func() {
		defer recoverGoroutinePanic("readLoop")
		session.readLoop()
	}()
	return session, nil
}

// Events 返回事件流。缓冲满时丢最旧的一帧——画面宁可掉帧也不要卡住读循环。
func (s *Session) Events() <-chan Event { return s.events }

// Close 关闭会话。
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
	})
}

func (s *Session) readLoop() {
	defer func() {
		s.Close()
		close(s.events)
	}()
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		if frame.ID != 0 {
			s.mu.Lock()
			ch, ok := s.pending[frame.ID]
			delete(s.pending, frame.ID)
			s.mu.Unlock()
			if ok {
				if frame.Error != nil {
					ch <- json.RawMessage(`{"__error":` + strconv.Quote(frame.Error.Message) + `}`)
				} else {
					ch <- frame.Result
				}
			}
			continue
		}
		if frame.Method == "" {
			continue
		}
		select {
		case s.events <- Event{Method: frame.Method, Params: frame.Params}:
		default:
			// 画面帧积压说明前端跟不上，丢掉最旧的一帧继续。
			select {
			case <-s.events:
			default:
			}
			select {
			case s.events <- Event{Method: frame.Method, Params: frame.Params}:
			default:
			}
		}
	}
}

// Call 发一条 CDP 命令并等结果。
func (s *Session) Call(ctx context.Context, method string, params map[string]any, out any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	err = s.conn.WriteMessage(websocket.TextMessage, body)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case result := <-ch:
		var failure struct {
			Error string `json:"__error"`
		}
		if json.Unmarshal(result, &failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(result, out)
	case <-s.closed:
		return errors.New("标签页连接已关闭")
	case <-ctx.Done():
		return ctx.Err()
	}
}
