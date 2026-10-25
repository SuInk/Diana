// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// cuaDriverEnv keeps the driver quiet and local: no product telemetry, no
// GitHub update checks. Diana pins the driver version itself.
var cuaDriverEnv = []string{
	"DO_NOT_TRACK=1",
	"CUA_DRIVER_RS_TELEMETRY_ENABLED=0",
	"CUA_TELEMETRY_ENABLED=false",
	"CUA_DRIVER_RS_UPDATE_CHECK=0",
}

// cuaDriverSession is one `cua-driver mcp` child process speaking MCP over
// stdio. It reconnects lazily after the process exits.
type cuaDriverSession struct {
	path    string
	args    []string
	timeout time.Duration

	mu      sync.Mutex
	session *mcpsdk.ClientSession
}

func newCUADriverSession(path string, args ...string) (*cuaDriverSession, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("cua-driver requires an absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("cua-driver is not executable")
	}
	if len(args) == 0 {
		args = []string{"mcp"}
	}
	return &cuaDriverSession{path: path, args: args, timeout: 30 * time.Second}, nil
}

func (s *cuaDriverSession) active(ctx context.Context) (*mcpsdk.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	cmd := procgroup.Isolate(exec.Command(s.path, s.args...))
	cmd.Env = append(os.Environ(), cuaDriverEnv...)
	cmd.Stderr = io.Discard
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "diana-desktopctl", Version: "1"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	session, err := client.Connect(connectCtx, &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		return nil, commandError(CodeHelper, "无法启动 cua-driver：%v", err)
	}
	s.session = session
	return session, nil
}

func (s *cuaDriverSession) drop(session *mcpsdk.ClientSession) {
	s.mu.Lock()
	if s.session == session {
		s.session = nil
	}
	s.mu.Unlock()
	_ = session.Close()
}

// call runs one tool and returns its structured content plus any image blocks.
// A tool-level error (IsError) is mapped to a protocol code; transport errors
// drop the session so the next call restarts the driver.
func (s *cuaDriverSession) call(ctx context.Context, name string, args map[string]any) (cuaDriverResult, error) {
	session, err := s.active(ctx)
	if err != nil {
		return cuaDriverResult{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		if ctx.Err() != nil {
			return cuaDriverResult{}, ctx.Err()
		}
		// Any transport-level failure (not a tool-level IsError) may mean the
		// driver died; restart it on the next call instead of reusing a dead pipe.
		s.drop(session)
		return cuaDriverResult{}, commandError(CodeHelper, "cua-driver %s 调用失败：%v", name, err)
	}
	out := cuaDriverResult{}
	if result.StructuredContent != nil {
		out.Structured, _ = json.Marshal(result.StructuredContent)
	}
	var texts []string
	for _, content := range result.Content {
		switch typed := content.(type) {
		case *mcpsdk.TextContent:
			texts = append(texts, typed.Text)
		case *mcpsdk.ImageContent:
			out.Images = append(out.Images, cuaDriverImage{Mime: typed.MIMEType, Data: typed.Data})
		}
	}
	out.Text = strings.Join(texts, "\n")
	if result.IsError {
		return out, cuaDriverToolError(name, out)
	}
	return out, nil
}

func (s *cuaDriverSession) Close() error {
	s.mu.Lock()
	session := s.session
	s.session = nil
	s.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

type cuaDriverImage struct {
	Mime string
	Data []byte
}

type cuaDriverResult struct {
	Structured json.RawMessage
	Text       string
	Images     []cuaDriverImage
}

// cuaDriverToolError maps the driver's refusals onto Diana's protocol codes so
// callers keep explaining failures the same way as with the native helper.
func cuaDriverToolError(name string, result cuaDriverResult) error {
	var payload struct {
		Error   any    `json:"error"`
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Refusal struct {
			Code string `json:"code"`
		} `json:"refusal"`
	}
	_ = json.Unmarshal(result.Structured, &payload)
	detail := strings.TrimSpace(result.Text)
	signal := strings.ToLower(payload.Code + " " + payload.Refusal.Code + " " + payload.Reason + " " + fmt.Sprint(payload.Error) + " " + detail)
	code := CodeHelper
	switch {
	// The driver's own permission rules refused the target or tool; that is an
	// app-scope refusal, not a missing macOS grant.
	case strings.Contains(signal, "outside the capability manifest") || strings.Contains(signal, "bounded_resource_outside_manifest"):
		code = CodeAppDenied
	case strings.Contains(signal, "permission") || strings.Contains(signal, "not trusted") || strings.Contains(signal, "tcc"):
		code = CodePermissionDenied
	case strings.Contains(signal, "stale") || strings.Contains(signal, "superseded"):
		code = CodeStaleObservation
	case strings.Contains(signal, "window_id_not_found") || strings.Contains(signal, "window_owner_pid_mismatch") || strings.Contains(signal, "no such process"):
		code = CodeWindowUnknown
	case strings.Contains(signal, "invalid") || strings.Contains(signal, "unknown_tool"):
		code = CodeBadRequest
	}
	if detail == "" {
		detail = "cua-driver 拒绝了 " + name
	}
	if len(detail) > 400 {
		detail = detail[:400] + "…"
	}
	return commandError(code, "%s", detail)
}
