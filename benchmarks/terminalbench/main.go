// Command terminalbench runs Diana's native agent inside an isolated benchmark container.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type input struct {
	Config      llm.ProviderConfig `json:"config"`
	Instruction string             `json:"instruction"`
}

type recordingClient struct {
	inner   llm.LLMClient
	log     *json.Encoder
	key     string
	usage   llm.Usage
	budget  int64
	perCall int64
}

func (c *recordingClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	remaining := c.budget - c.usage.OutputTokens
	if remaining <= 0 {
		return nil, errors.New("benchmark output token budget exhausted")
	}
	if c.perCall > 0 && remaining > c.perCall {
		remaining = c.perCall
	}
	if req.MaxOutputTokens <= 0 || req.MaxOutputTokens > remaining {
		req.MaxOutputTokens = remaining
	}
	started := time.Now()
	res, err := c.inner.Generate(ctx, req)
	if res != nil {
		c.usage.Add(res.Usage)
	}
	event := map[string]any{"kind": "model", "request": req, "response": res, "duration_ms": time.Since(started).Milliseconds()}
	if err != nil {
		event["error"] = err.Error()
	}
	writeRedacted(c.log, event, c.key)
	return res, err
}

func writeRedacted(enc *json.Encoder, v any, key string) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if key != "" {
		b = []byte(strings.ReplaceAll(string(b), key, "[REDACTED]"))
	}
	_ = enc.Encode(json.RawMessage(b))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	workdir := flag.String("workdir", ".", "native tools workspace")
	logPath := flag.String("log", "/logs/agent/diana.jsonl", "JSONL event log")
	seconds := flag.Int("timeout", 900, "task deadline in seconds")
	steps := flag.Int("steps", agent.MaxAllowedSteps, "native tool-call budget (subject to Diana's hard limit)")
	budget := flag.Int64("output-budget", 32768, "aggregate output token ceiling")
	flag.Parse()
	// This entry point enables shell execution; never launch it on the user's host.
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return errors.New("terminalbench must run inside a disposable Docker task container")
	}
	if *seconds <= 0 || *budget <= 0 {
		return errors.New("timeout and output budget must be positive")
	}
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("decode benchmark input: %w", err)
	}
	if strings.TrimSpace(in.Instruction) == "" {
		return errors.New("missing task instruction")
	}
	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	enc := json.NewEncoder(logFile)
	client, err := llm.NewClient(in.Config)
	if err != nil {
		return errors.New("invalid benchmark model configuration")
	}
	recorder := &recordingClient{inner: client, log: enc, key: in.Config.APIKey, budget: *budget, perCall: in.Config.MaxOutputTokens}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*seconds)*time.Second)
	defer cancel()
	// Docker is the isolation boundary. Keep the native runner, prompt, schemas,
	// protocol repair and tool implementations intact; no benchmark-specific solver.
	cfg := agent.Config{WorkDir: *workdir, MaxSteps: *steps, FileWriteEnabled: true,
		BrowserToolsDisabled: true, CommandAllowlist: []string{"*"},
		CommandSandbox: agent.CommandSandboxOff, CommandTimeoutMS: 60000}
	cfg = cfg.WithDefaults()
	writeRedacted(enc, map[string]any{"kind": "config", "model": in.Config.Model,
		"reasoning_effort": in.Config.ReasoningEffort, "max_steps": cfg.MaxSteps,
		"timeout_seconds": *seconds, "output_budget": *budget, "workdir": *workdir}, in.Config.APIKey)
	registry, err := agent.NewDefaultToolRegistry(cfg)
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(recorder, cfg, registry)
	if err != nil {
		_ = registry.Close()
		return err
	}
	defer runner.Close()
	res, runErr := runner.Run(ctx, agent.Request{Messages: []llm.Message{taskMessage(in.Instruction)},
		Observer: func(_ context.Context, event agent.RunEvent) {
			writeRedacted(enc, map[string]any{"kind": "event", "event": event}, in.Config.APIKey)
		}})
	final := map[string]any{"kind": "result", "response": res, "usage": recorder.usage}
	if runErr != nil {
		final["error"] = runErr.Error()
	}
	writeRedacted(enc, final, in.Config.APIKey)
	if runErr != nil {
		return errors.New("agent run failed; inspect redacted event log")
	}
	return nil
}

func taskMessage(instruction string) llm.Message {
	// Match the production assistant input path: keep the current user task
	// protected after tool results become the most recent messages.
	return llm.Message{Role: llm.RoleUser, Content: instruction, Priority: llm.MessagePriorityCurrent, AtomicText: true}
}
