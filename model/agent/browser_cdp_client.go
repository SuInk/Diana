// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cdpClient 是连到一个标签页的 CDP 会话。
//
// 读放在单独的 goroutine 里，每次调用只等自己那一条回复。以前靠连接的读期限做超时，
// 一次超时整条连接就废了（gorilla 读出错之后不能再读），页面脚本卡死时连一条
// Runtime.terminateExecution 都补不上；而新连上来的会话挂不到卡住的渲染进程上，
// 只有这条在页面卡住之前就挂好的会话能把死循环打断。
type cdpClient struct {
	conn   *websocket.Conn
	nextID atomic.Int64
	// timeout 是浏览器超时（agent_browser_timeout_ms），也是单次调用默认最多等多久：
	// 页面主线程被脚本卡死或者渲染进程崩了，Runtime.evaluate 永远不回，不设上限就
	// 一直耗到 Runner 的工具总超时。
	timeout time.Duration
	// callLimit 非零时代替 timeout 作单次调用的上限，browser_wait、browser_eval 这类
	// 本来就要等的按自己的参数放宽。
	callLimit time.Duration
	// deadline 非零时，之后的调用都不许越过它；browser_open 用它把导航之后的等待和
	// 取快照整体收进一个浏览器超时里。budget 是报给模型的那段时长。
	deadline time.Time
	budget   time.Duration
	// wsURL 留着给恢复用：卡死或崩溃之后另开一条会话把标签页导航回空白页。
	wsURL    string
	baseURL  string
	targetID string

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan cdpResponse
	// failure 记着这条会话撞上的卡死或崩溃。记上之后普通调用直接失败，不再一条条
	// 各等一个超时。
	failure *browserPageError
	readErr error
	done    chan struct{}
	once    sync.Once
}

type cdpResponse struct {
	result json.RawMessage
	err    error
}

// cdpRemoteError 是浏览器对一条命令回的错误，区别于连接断开和超时。
type cdpRemoteError struct{ message string }

func (e *cdpRemoteError) Error() string { return e.message }

func newCDPClient(ctx context.Context, websocketURL string, timeout time.Duration) (*cdpClient, error) {
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	if timeout <= 0 {
		dialer.HandshakeTimeout = DefaultBrowserTimeoutMS * time.Millisecond
	}
	conn, _, err := dialer.DialContext(ctx, websocketURL, nil)
	if err != nil {
		return nil, err
	}
	c := &cdpClient{
		conn:    conn,
		timeout: timeout,
		wsURL:   websocketURL,
		pending: map[int64]chan cdpResponse{},
		done:    make(chan struct{}),
	}
	go func() {
		defer recoverGoroutinePanic("cdp_client.readLoop")
		c.readLoop()
	}()
	return c, nil
}

func (c *cdpClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	var err error
	c.once.Do(func() { err = c.conn.Close() })
	return err
}

func (c *cdpClient) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			response := cdpResponse{result: msg.Result}
			if msg.Error != nil {
				response.err = &cdpRemoteError{message: msg.Error.Message}
			}
			c.deliver(msg.ID, response)
			continue
		}
		// 渲染进程崩了之后发给页面的命令一条也不会回。事件一到就让所有在等的调用
		// 立刻失败，不要各自等满超时。对已经崩掉的标签页，Inspector.enable 一开就会
		// 先推一条这个事件。
		if msg.Method == "Inspector.targetCrashed" {
			c.fail(&browserPageError{crashed: true})
		}
	}
}

func (c *cdpClient) deliver(id int64, response cdpResponse) {
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
}

// fail 记下页面故障并让在等的调用都拿到它。只记第一次：崩溃之后跟着来的超时不该
// 把「崩溃」改写成「没响应」。
func (c *cdpClient) fail(failure *browserPageError) *browserPageError {
	c.mu.Lock()
	if c.failure == nil {
		c.failure = failure
	}
	failure = c.failure
	waiting := c.pending
	c.pending = map[int64]chan cdpResponse{}
	c.mu.Unlock()
	for _, ch := range waiting {
		select {
		case ch <- cdpResponse{err: failure}:
		default:
		}
	}
	return failure
}

// pageFailure 返回这条会话撞上的卡死或崩溃，没有时返回 nil。
func (c *cdpClient) pageFailure() *browserPageError {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// minCDPCallWait 是整体期限（limitFor）到了之后，单条调用至少还能等多久。
const minCDPCallWait = time.Second

// remaining 是离整体期限还有多久；没设期限时是一个足够大的值。
func (c *cdpClient) remaining() time.Duration {
	if c.deadline.IsZero() {
		return time.Hour
	}
	return time.Until(c.deadline)
}

// limitFor 让之后的调用整体不超过 budget。
func (c *cdpClient) limitFor(budget time.Duration) {
	if budget <= 0 {
		return
	}
	c.deadline = time.Now().Add(budget)
	c.budget = budget
}

// send 只发不等。Page.enable、Runtime.enable 要渲染进程回，页面卡死时等它们就是
// 每个工具先白等一个浏览器超时；而同一会话里的命令按顺序处理，后面的调用不会跑在
// 它们前面。
func (c *cdpClient) send(method string) {
	_ = c.write(context.Background(), c.nextID.Add(1), method, nil)
}

func (c *cdpClient) write(ctx context.Context, id int64, method string, params map[string]any) error {
	if params == nil {
		params = map[string]any{}
	}
	deadline := time.Now().Add(browserAbortTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(deadline)
	return c.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params})
}

// roundTrip 发一条命令并等它的回复，不看 failure、不加上限：恢复用的命令直接走它。
func (c *cdpClient) roundTrip(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan cdpResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.write(ctx, id, method, params); err != nil {
		return nil, err
	}
	select {
	case response := <-ch:
		return response.result, response.err
	case <-c.done:
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("标签页连接断开：%w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *cdpClient) call(ctx context.Context, method string, params map[string]any, out any) error {
	if failure := c.pageFailure(); failure != nil {
		return failure
	}
	limit := c.timeout
	if c.callLimit > 0 {
		limit = c.callLimit
	}
	stuckAfter := limit
	if !c.deadline.IsZero() {
		// 期限快到或已过时仍给这一条留一点时间：卡死要靠「发了没人回」来认，不能
		// 因为前面的轮询用光了时间就把一个好好的页面报成卡死。
		remaining := max(time.Until(c.deadline), minCDPCallWait)
		if limit <= 0 || remaining < limit {
			limit = remaining
			stuckAfter = c.budget
		}
	}
	callCtx := ctx
	capped := false
	if limit > 0 {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > limit {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, limit)
			defer cancel()
			capped = true
		}
	}
	result, err := c.roundTrip(callCtx, method, params)
	if err != nil {
		var failure *browserPageError
		switch {
		case errors.As(err, &failure):
			return failure
		case ctx.Err() != nil:
			// 调用方自己的期限到了或被取消：原样交回，Runner 据此认它自己的超时。
			return fmt.Errorf("cdp %s: %w", method, ctx.Err())
		case capped && callCtx.Err() != nil:
			return c.fail(&browserPageError{timeout: stuckAfter})
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			return fmt.Errorf("cdp %s: %w", method, err)
		}
		var remote *cdpRemoteError
		if errors.As(err, &remote) {
			return fmt.Errorf("cdp %s failed: %w", method, remote)
		}
		return err
	}
	if out != nil && len(result) > 0 {
		return json.Unmarshal(result, out)
	}
	return nil
}

func (c *cdpClient) evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	if len(out.Result.Value) == 0 {
		return []byte("null"), nil
	}
	return compactCDPValue(out.Result.Value), nil
}
