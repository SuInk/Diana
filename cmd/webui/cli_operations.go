package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

func runStatusCommand(args []string, output io.Writer) error {
	config, path, err := loadCLIConfig(args)
	if err != nil {
		return err
	}
	address := healthAddress(config)
	health, err := fetchHealth(context.Background(), address)
	if err != nil {
		return fmt.Errorf("Diana is not reachable at %s: %w", address, err)
	}
	_, err = fmt.Fprintf(output, "Status:  %s\nVersion: %s\nAddress: %s\nUptime:  %s\nConfig:  %s\n", health.Status, health.Version, address, formatUptime(health.UptimeSeconds), path)
	return err
}

func runRestartCommand(args []string, output io.Writer) error {
	config, path, err := loadCLIConfig(args)
	if err != nil {
		return err
	}
	root, err := installationRoot()
	if err != nil {
		return err
	}
	if err := restartInstalledService(root, path); err != nil {
		return err
	}
	address := healthAddress(config)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		if _, err := fetchHealth(ctx, address); err == nil {
			_, err = fmt.Fprintf(output, "Diana restarted and is healthy at %s\n", address)
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Diana restart was requested but health did not recover at %s", address)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func runDoctorCommand(args []string, output io.Writer) error {
	config, path, err := loadCLIConfig(args)
	if err != nil {
		return err
	}
	failures := 0
	check := func(ok bool, success, failure string) {
		if ok {
			_, _ = fmt.Fprintln(output, "[ok]   "+success)
		} else {
			failures++
			_, _ = fmt.Fprintln(output, "[fail] "+failure)
		}
	}
	check(path != "", "config: "+path, "config.yaml was not found")
	port := stringOr(config.Server.Port, "18080")
	portNumber, portErr := strconv.Atoi(port)
	check(portErr == nil && portNumber > 0 && portNumber <= 65535, "port: "+port, "invalid server.port: "+port)
	if config.Storage.DBPath != "" {
		directory := filepath.Dir(resolveConfigRelative(path, config.Storage.DBPath))
		check(directoryWritable(directory), "database directory writable: "+directory, "database directory is not writable: "+directory)
	}
	if config.Storage.LogPath != "" {
		directory := filepath.Dir(resolveConfigRelative(path, config.Storage.LogPath))
		check(directoryWritable(directory), "log directory writable: "+directory, "log directory is not writable: "+directory)
	}
	frontendSetting := strings.TrimSpace(config.Server.FrontendDist)
	if frontendSetting != "" {
		frontendSetting = resolveConfigRelative(path, frontendSetting)
	}
	frontend := frontendDistDir(frontendSetting)
	_, frontendErr := os.Stat(filepath.Join(frontend, "index.html"))
	check(frontendErr == nil, "frontend assets: "+frontend, "frontend assets are missing: "+frontend)
	address := healthAddress(config)
	health, healthErr := fetchHealth(context.Background(), address)
	if healthErr == nil {
		check(true, "service healthy: "+health.Version+" at "+address, "")
	} else {
		_, _ = fmt.Fprintln(output, "[warn] service is not reachable at "+address)
	}
	if failures > 0 {
		return fmt.Errorf("doctor found %d blocking issue(s)", failures)
	}
	_, err = fmt.Fprintln(output, "Doctor completed without blocking issues.")
	return err
}

func runConfigCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("config requires path or check")
	}
	subcommand := args[0]
	config, path, err := loadCLIConfig(args[1:])
	if err != nil {
		return err
	}
	switch subcommand {
	case "path":
		_, err = fmt.Fprintln(output, path)
		return err
	case "check":
		if _, _, err := config.botSeedConfig(defaultOneBotEndpoint(stringOr(config.Server.Port, "18080"))); err != nil {
			return err
		}
		if _, _, err := config.llmSeedConfig(); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Configuration is valid: "+path)
		return err
	default:
		return fmt.Errorf("unknown config command: %s", subcommand)
	}
}

func loadCLIConfig(args []string) (appConfig, string, error) {
	path := ""
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--config":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return appConfig{}, "", fmt.Errorf("--config requires a path")
			}
			path = args[index]
		case strings.HasPrefix(argument, "--config="):
			path = strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
		default:
			return appConfig{}, "", fmt.Errorf("unknown option: %s", argument)
		}
	}
	if path == "" {
		path = resolveConfigPath(nil)
	}
	if path == "" {
		return appConfig{}, "", fmt.Errorf("config.yaml was not found; pass --config to locate it")
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
		return appConfig{}, "", fmt.Errorf("config file does not exist: %s", path)
	}
	config, err := loadAppConfig(path)
	return config, path, err
}

func healthAddress(config appConfig) string {
	return "http://" + net.JoinHostPort(displayHost(config.Server.Host), stringOr(config.Server.Port, "18080")) + "/api/health"
}

func fetchHealth(parent context.Context, address string) (healthResponse, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return healthResponse{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return healthResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return healthResponse{}, fmt.Errorf("HTTP %s", response.Status)
	}
	var health healthResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&health); err != nil {
		return health, err
	}
	if health.Status != "ok" {
		return health, fmt.Errorf("unexpected health status %q", health.Status)
	}
	return health, nil
}

func resolveConfigRelative(configPath, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}

func directoryWritable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	file, err := os.CreateTemp(path, ".diana-doctor-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

func formatUptime(seconds int64) string {
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}
