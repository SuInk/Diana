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

// runResetPasswordCommand 在忘记管理员密码时离线重置：生成新的随机密码，清空
// 全部登录会话，其余数据不动。必须在服务停止时执行——运行中的进程内存里还留着
// 旧凭据和旧会话，并会把旧会话写回数据库。
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
	lock, err := acquireInstanceLock(dbPath, "")
	if err != nil {
		if errors.Is(err, errInstanceLocked) {
			return fmt.Errorf("Diana is still running with the data at %s; stop it first, then run `diana passwd` again%s", dbPath, resetPasswordStopHint())
		}
		return err
	}
	defer lock.Release()
	// 确认放在确认服务已停止之后：先问了再报“服务还在运行”，等于白问。
	if !options.yes {
		if !prompt.interactive {
			return fmt.Errorf("passwd needs confirmation; run it in a terminal, or pass --yes to skip the prompt")
		}
		_, _ = fmt.Fprintf(output, "This resets the Diana administrator password for %s and signs out every WebUI session.\nOther data is not changed. Continue? [y/N] ", dbPath)
		answer, _ := bufio.NewReader(prompt.input).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			_, err := fmt.Fprintln(output, "Cancelled; nothing was changed.")
			return err
		}
	}

	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", dbPath, err)
	}
	defer func() {
		_ = store.Close()
	}()
	result, err := webui.NewAuthManager(store).ResetCredentials(options.username)
	if err != nil {
		return err
	}
	// 清会话的写入失败只会静默留下旧会话，这里读回来确认，宁可报错也不假装已全部登出。
	if sessions, ok, err := store.LoadWebUISessions(context.Background()); err != nil {
		return fmt.Errorf("password was reset but sessions could not be verified: %w", err)
	} else if ok && len(sessions.Sessions) > 0 {
		return fmt.Errorf("password was reset but %d old session(s) are still stored; run `diana passwd` again", len(sessions.Sessions))
	}
	_, err = fmt.Fprintf(output, "Diana administrator credentials were reset\n  username: %s\n  password: %s\nAll existing WebUI sessions were signed out. Start Diana and sign in with these credentials.\n", result.Username, result.GeneratedPassword)
	if err == nil && strings.TrimSpace(config.Admin.Password) != "" {
		// admin 段只在数据库为空时播种，一键安装写下的旧密码不会再生效，提醒一句免得照着它登录。
		_, err = fmt.Fprintf(output, "Note: admin.password in %s is the old initial password and no longer applies.\n", config.path)
	}
	return err
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
		return " (Docker: on the host run `docker compose stop diana`, then `docker compose run --rm diana passwd`, then `docker compose start diana`)"
	}
	return " (for example `sudo systemctl stop diana` or `systemctl --user stop diana`; start it again afterwards)"
}

// stdinIsTerminal 报告标准输入是不是终端，决定命令行能不能当面问用户。
// /dev/null 也是字符设备，算作可交互无妨：读到空行按取消处理。
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
