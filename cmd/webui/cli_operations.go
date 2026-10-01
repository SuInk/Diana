package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	_, err = fmt.Fprintf(output, "Status:  %s\nVersion: %s\nAddress: %s\nUptime:  %s\nConfig:  %s\n", health.Status, health.Version, address, formatUptime(health.UptimeSeconds), configPathLabel(path))
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
	if err := waitForHealth(address, 20*time.Second); err != nil {
		return fmt.Errorf("Diana restart was requested but health did not recover at %s", address)
	}
	_, err = fmt.Fprintf(output, "Diana restarted and is healthy at %s\n", address)
	return err
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
	if path == "" {
		// 没有配置文件是合法部署（Docker 默认就是这样），服务按内置默认值运行。
		_, _ = fmt.Fprintln(output, "[warn] config: "+configPathLabel(path))
	} else {
		check(true, "config: "+path, "")
	}
	port := stringOr(config.Server.Port, "18080")
	portNumber, portErr := strconv.Atoi(port)
	check(portErr == nil && portNumber > 0 && portNumber <= 65535, "port: "+port, "invalid server.port: "+port)
	dbSetting := strings.TrimSpace(config.Storage.DBPath)
	if dbSetting == "" && path == "" {
		// 没有配置文件时服务用默认的 data/diana.db，也照样检查。
		dbSetting = filepath.Join("data", "diana.db")
	}
	if dbSetting != "" {
		directory := filepath.Dir(resolveConfigRelative(path, dbSetting))
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
		if path == "" {
			return fmt.Errorf("no config.yaml is in use; Diana runs on built-in defaults (create data/config.yaml or set %s to add one)", configPathEnv)
		}
		_, err = fmt.Fprintln(output, path)
		return err
	case "check":
		if _, _, err := config.botSeedConfig(defaultOneBotEndpoint(stringOr(config.Server.Port, "18080"))); err != nil {
			return err
		}
		if _, _, err := config.llmSeedConfig(); err != nil {
			return err
		}
		if err := config.validateAdmin(); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Configuration is valid: "+configPathLabel(path))
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
		// 和服务本身一致：找不到配置文件就用内置默认值，而不是让命令行直接报错。
		config, err := loadAppConfig("")
		return config, "", err
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

func configPathLabel(path string) string {
	if path == "" {
		return "none (built-in defaults)"
	}
	return path
}

func healthAddress(config appConfig) string {
	return webuiAddress(config) + "/api/health"
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

// resolveConfigRelative 解析配置里的相对路径，供命令行工具使用。命令行在用户自己
// 的 shell 里跑，工作目录不一定是服务的，所以按配置文件所在目录算——一键安装和
// 旧版 Docker 布局里它就是服务的工作目录。官方 Docker 镜像例外：docker exec 的
// 工作目录就是镜像 WORKDIR，和主程序一致，而配置文件可能放在 data/config.yaml，
// 按它的目录算会指到服务根本不用的 data/data。
func resolveConfigRelative(configPath, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	if dockerDeployment() {
		if absolute, err := filepath.Abs(value); err == nil {
			return absolute
		}
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
