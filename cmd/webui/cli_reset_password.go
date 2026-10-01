// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuInk/diana/model/storage"
	"github.com/SuInk/diana/webui"
)

type resetPasswordOptions struct {
	username   string
	configPath string
	yes        bool
}

// cliPrompt 是命令行确认用的输入端；interactive 为假时（脚本、管道）不能提问。
type cliPrompt struct {
	input       io.Reader
	interactive bool
}

// runResetPasswordCommand 在忘记管理员密码时重置：生成新的随机密码，清空全部
// 登录会话，其余数据不动。重置只能在服务停止时做——运行中的进程内存里还留着旧
// 凭据和旧会话，并会把旧会话写回数据库。服务是一键安装注册的，就先停掉、改完再
// 启动；别的方式跑起来的（Docker、手动运行）不知道怎么再拉起来，只能请用户自己停。
func runResetPasswordCommand(args []string, prompt cliPrompt, output io.Writer) error {
	options, err := parseResetPasswordOptions(args)
	if err != nil {
		return err
	}
	configPath := options.configPath
	if configPath == "" {
		configPath = resolveConfigPath(nil)
	}
	config, err := loadAppConfig(configPath)
	if err != nil {
		return err
	}
	dbPath, err := cliDatabasePath(config)
	if err != nil {
		return err
	}
	if err := checkDatabaseOwner(dbPath); err != nil {
		return err
	}
	var service *installedService
	lock, err := acquireInstanceLock(dbPath, "")
	if err != nil {
		if !errors.Is(err, errInstanceLocked) {
			return err
		}
		if !dockerDeployment() {
			if root, rootErr := installationRoot(); rootErr == nil {
				service = findInstalledServiceFunc(root, dbPath, config.path)
			}
		}
		if service == nil {
			return fmt.Errorf("Diana is still running with the data at %s. Stop it, run `diana passwd`, then start it again%s", dbPath, resetPasswordStopHint())
		}
	}
	// 确认放在弄清服务状态之后：要停服务得先说清楚，停不了的情况也不该白问一遍。
	if !options.yes {
		if !prompt.interactive {
			if lock != nil {
				lock.Release()
			}
			return fmt.Errorf("`diana passwd` needs confirmation: run it in a terminal, or add -y to skip the prompt")
		}
		renamed := ""
		if options.username != "" {
			renamed = fmt.Sprintf("  - The username becomes %q\n", options.username)
		}
		restart := ""
		if service != nil {
			restart = fmt.Sprintf("  - Diana (%s) is stopped now and started again afterwards\n", service.name)
		}
		_, _ = fmt.Fprintf(output, "Reset the Diana administrator password?\n  - A new random password is generated and shown once\n%s  - Every WebUI session is signed out\n%s  - Nothing else is changed\nDatabase: %s\nContinue? [y/N] ", renamed, restart, dbPath)
		answer, _ := bufio.NewReader(prompt.input).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			if lock != nil {
				lock.Release()
			}
			_, err := fmt.Fprintln(output, "Cancelled. Nothing was changed.")
			return err
		}
	}

	if service != nil {
		_, _ = fmt.Fprintln(output, "Stopping Diana...")
		if err := service.stop(); err != nil {
			return fmt.Errorf("stop Diana (%s): %w", service.name, err)
		}
		lock, err = waitForInstanceLock(dbPath, serviceStopTimeout)
		if err != nil {
			// 没停下来就别动数据库；把服务恢复到原来的状态再报错。
			_ = service.start()
			return fmt.Errorf("Diana did not stop within %s, so nothing was changed: %w", serviceStopTimeout, err)
		}
	}
	result, resetErr := resetCredentialsLocked(dbPath, options.username)
	// 先放锁再启动，否则新进程拿不到实例锁。重置失败也要把服务拉起来，不能留着停机。
	lock.Release()
	if resetErr == nil {
		_, _ = fmt.Fprintf(output, "Administrator password reset. Save it now; it is not shown again.\n  username: %s\n  password: %s\n", result.Username, result.GeneratedPassword)
		if strings.TrimSpace(config.Admin.Password) != "" {
			// admin 段只在数据库为空时播种，一键安装写下的旧密码不会再生效，提醒一句免得照着它登录。
			_, _ = fmt.Fprintf(output, "Note: admin.password in %s is no longer used; sign in with the password above.\n", config.path)
		}
	}
	if service == nil {
		if resetErr != nil {
			return resetErr
		}
		_, err = fmt.Fprintln(output, "Start Diana and sign in with these credentials.")
		return err
	}
	_, _ = fmt.Fprintln(output, "Starting Diana...")
	startErr := service.start()
	if startErr == nil {
		startErr = waitForHealth(healthAddress(config), serviceStartTimeout)
	}
	if resetErr != nil {
		if startErr != nil {
			return fmt.Errorf("%w; Diana could not be started again either: %v", resetErr, startErr)
		}
		return resetErr
	}
	if startErr != nil {
		return fmt.Errorf("password was reset, but Diana did not start again (%s): %w; start it manually", service.name, startErr)
	}
	_, err = fmt.Fprintf(output, "Diana is running again at %s. Sign in with these credentials.\n", webuiAddress(config))
	return err
}

// resetCredentialsLocked 在已持有实例锁时改写凭据并确认旧会话已清空。
func resetCredentialsLocked(dbPath, username string) (webui.AuthBootstrapResult, error) {
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		return webui.AuthBootstrapResult{}, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	defer func() {
		_ = store.Close()
	}()
	result, err := webui.NewAuthManager(store).ResetCredentials(username)
	if err != nil {
		return result, err
	}
	// 清会话的写入失败只会静默留下旧会话，这里读回来确认，宁可报错也不假装已全部登出。
	if sessions, ok, err := store.LoadWebUISessions(context.Background()); err != nil {
		return result, fmt.Errorf("password was reset but sessions could not be verified: %w", err)
	} else if ok && len(sessions.Sessions) > 0 {
		return result, fmt.Errorf("password was reset but %d old session(s) are still stored; run `diana passwd` again", len(sessions.Sessions))
	}
	return result, nil
}

func parseResetPasswordOptions(args []string) (resetPasswordOptions, error) {
	var options resetPasswordOptions
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--username" || argument == "--config":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return options, fmt.Errorf("%s requires a value", argument)
			}
			if argument == "--username" {
				options.username = strings.TrimSpace(args[index])
			} else {
				options.configPath = strings.TrimSpace(args[index])
			}
		case strings.HasPrefix(argument, "--username="):
			options.username = strings.TrimSpace(strings.TrimPrefix(argument, "--username="))
			if options.username == "" {
				return options, fmt.Errorf("--username requires a value")
			}
		case strings.HasPrefix(argument, "--config="):
			options.configPath = strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
			if options.configPath == "" {
				return options, fmt.Errorf("--config requires a path")
			}
		case argument == "--yes" || argument == "-y":
			options.yes = true
		default:
			return options, fmt.Errorf("unknown passwd option: %s", argument)
		}
	}
	return options, nil
}

// cliDatabasePath 找到服务实际使用的数据库。服务按自己的工作目录解析相对路径，
// 命令行的工作目录不一定相同：先按配置文件所在目录算（一键安装里它就是服务的
// 工作目录），找不到再按当前目录算（Docker 和 data/config.yaml 的布局）。只接受
// 已存在的文件，绝不在错误的位置新建一个空库。
func cliDatabasePath(config appConfig) (string, error) {
	setting := strings.TrimSpace(config.Storage.DBPath)
	if setting == ":memory:" || strings.HasPrefix(setting, "file:") {
		return "", fmt.Errorf("storage.db_path %q is not a database file; passwd cannot change it", setting)
	}
	if setting == "" {
		setting = filepath.Join("data", "diana.db")
	}
	var candidates []string
	if config.path != "" {
		candidates = append(candidates, resolveConfigRelative(config.path, setting))
	}
	if absolute, err := filepath.Abs(setting); err == nil {
		candidates = append(candidates, absolute)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("cannot resolve database path %s", setting)
	}
	return "", fmt.Errorf("database was not found at %s; start Diana once first, or pass --config to point at its config.yaml", candidates[0])
}

func resetPasswordStopHint() string {
	if dockerDeployment() {
		return ". On Docker, run on the host: docker compose stop diana && docker compose run --rm diana passwd && docker compose start diana"
	}
	return " (it was not started by the Diana installer, so `diana passwd` cannot restart it for you)"
}

// stdinIsTerminal 报告标准输入是不是终端，决定命令行能不能当面问用户。
// /dev/null 也是字符设备，算作可交互无妨：读到空行按取消处理。
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
