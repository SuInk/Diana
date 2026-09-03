// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// findObscuraExecutable locates Diana's on-demand fallback renderer without
// putting its private install directory on the service-wide PATH.
func findObscuraExecutable(configured string) (string, error) {
	configured = strings.TrimSpace(firstNonEmptyString(configured, os.Getenv("DIANA_OBSCURA_EXECUTABLE")))
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return configured, nil
		}
		if path, err := exec.LookPath(configured); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("configured obscura executable not found: %s", configured)
	}
	if path, err := exec.LookPath("obscura"); err == nil {
		return path, nil
	}
	configDir, err := os.UserConfigDir()
	if err == nil {
		name := "obscura"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(configDir, "diana", "tools", "obscura", "v0.2.1", name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return candidate, nil
		}
	}
	return "", errors.New("Obscura executable was not found")
}

func looksLikeObscuraExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return name == "obscura" || name == "obscura.exe"
}

func probeObscura(ctx context.Context, path string) HeadlessBrowserStatus {
	probeCtx, cancel := context.WithTimeout(ctx, headlessBrowserProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		return HeadlessBrowserStatus{Path: path, Engine: "obscura", Detail: "找到了 " + path + "，但它执行失败：" + compactBrowserError(string(output))}
	}
	return HeadlessBrowserStatus{
		Available: true,
		Path:      path,
		Version:   strings.TrimSpace(firstNonEmptyLine(string(output))),
		Engine:    "obscura",
	}
}

func renderWithObscura(ctx context.Context, executable, rawURL string, cfg SandboxedBrowserConfig) (RenderedPage, error) {
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	timeoutSeconds := max(1, int(cfg.Timeout.Round(time.Second)/time.Second))
	args := []string{
		"fetch", rawURL,
		"--dump", "html",
		"--wait-until", "networkidle0",
		"--timeout", strconv.Itoa(timeoutSeconds),
		"--user-agent", BrowserUserAgent,
		"--quiet",
	}
	output := &cappedBuffer{limit: cfg.MaxHTMLBytes}
	diagnostics := &cappedBuffer{limit: 32 * 1024}
	command := exec.CommandContext(runCtx, executable, args...)
	command.Stdout = output
	command.Stderr = diagnostics
	if err := command.Run(); err != nil {
		if runCtx.Err() != nil {
			return RenderedPage{}, fmt.Errorf("obscura render timeout: %w", runCtx.Err())
		}
		return RenderedPage{}, fmt.Errorf("obscura render failed: %w: %s", err, compactBrowserError(diagnostics.String()))
	}
	page, err := parseRenderedPage(output.Bytes(), rawURL, cfg.MaxTextChars, output.truncated)
	if err != nil {
		return RenderedPage{}, err
	}
	page.Sandboxed = true
	page.BrowserEngine = "obscura"
	page.ReadyState = "complete"
	page.Stable = true
	page.StabilityReason = "obscura_settled"
	page.WaitedMS = time.Since(started).Milliseconds()
	page.NavigationChain = []string{rawURL}
	if page.URL != "" && page.URL != rawURL {
		page.NavigationChain = append(page.NavigationChain, page.URL)
	}
	return page, nil
}
