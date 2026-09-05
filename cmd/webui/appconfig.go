// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"

	"go.yaml.in/yaml/v4"
)

// 配置分两层，边界就是「这项能不能在 WebUI 里改」：
//
//   - server / storage / admin / update / napcat 是基础设施，WebUI 里没有对应
//     入口，config.yaml 是唯一来源，每次启动都读。
//   - bot / llm 是业务配置，唯一真相源是数据库，WebUI 随时可改。config.yaml
//     里的这两段只在数据库为空时用作首启播种，供无人值守部署使用。
//
// 之前这两层都堆在环境变量里，而业务那层在首启之后会静默失效——改了 .env 重启
// 没反应，也不报错。所以现在业务段每次启动都和数据库对账，不一致就打日志说明
// 以 WebUI 为准，不再让人对着一份不生效的配置排查。
type appConfig struct {
	Server  serverConfig  `yaml:"server"`
	Storage storageConfig `yaml:"storage"`
	Admin   adminConfig   `yaml:"admin"`
	Update  updateConfig  `yaml:"update"`
	NapCat  napcatConfig  `yaml:"napcat"`
	// Bot 和 LLM 用 yaml.Node 收着，后面按 JSON tag 解码，好让 config.yaml 的
	// 字段名和 WebUI 接口的 payload 完全一致，不用维护第二套字段名。
	Bot yaml.Node `yaml:"bot"`
	LLM yaml.Node `yaml:"llm"`

	// path 记录这份配置从哪读来的，日志和错误信息里要说清楚。
	path string
}

type serverConfig struct {
	Host           string   `yaml:"host"`
	Port           string   `yaml:"port"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	FrontendDist   string   `yaml:"frontend_dist"`
}

type storageConfig struct {
	DBPath string `yaml:"db_path"`
	// Zero uses defaults; -1 keeps logs indefinitely.
	DebugLogRetentionDays int `yaml:"debug_log_retention_days"`
	LogRetentionDays      int `yaml:"log_retention_days"`
	// YAML fallback until a WebUI cache policy is saved. Zero days uses seven
	// days; -1 disables expiry. Zero MB disables the cap.
	DownloadCacheRetentionDays int   `yaml:"download_cache_retention_days"`
	DownloadCacheMaxMB         int64 `yaml:"download_cache_max_mb"`
	// LogPath 为空表示只写标准输出。
	LogPath string `yaml:"log_path"`
	// MediaDir 为空表示放在数据库同级目录。
	MediaDir string `yaml:"media_dir"`
	// MediaMaxMB / MediaCacheMB 为 0 表示用内置默认值。
	MediaMaxMB   int `yaml:"media_max_mb"`
	MediaCacheMB int `yaml:"media_cache_mb"`
	// LocalMediaBaseURL 为空时按反连握手地址动态推断，见 main。
	LocalMediaBaseURL string `yaml:"local_media_base_url"`
}

type adminConfig struct {
	// 两项都留空时首启自动生成账号和强密码，只打印一次。
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type updateConfig struct {
	Root           string `yaml:"root"`
	ApplyEnabled   *bool  `yaml:"apply_enabled"`
	ReleaseEnabled *bool  `yaml:"release_enabled"`
	GroupTest      *bool  `yaml:"group_test_enabled"`
}

type napcatConfig struct {
	WebUIURL   string `yaml:"webui_url"`
	WebUIToken string `yaml:"webui_token"`
}

// configPathEnv 是唯一保留的环境变量：它不是配置，是指向配置文件的引导指针。
// 容器里挂载路径各不相同，总得有个办法告诉进程去哪找 config.yaml。
const configPathEnv = "DIANA_CONFIG"

// defaultConfigFileName 是不指定路径时按约定查找的文件名。
const defaultConfigFileName = "config.yaml"

// resolveConfigPath 按 --config、DIANA_CONFIG、工作目录、可执行文件目录的顺序
// 找配置文件。命令入口经常是 /usr/local/bin 或 ~/.local/bin 下的符号链接，
// 所以还要检查链接指向的真实安装目录。返回空字符串表示哪里都没有，此时全部
// 走内置默认值。
func resolveConfigPath(args []string) string {
	if explicit := configPathFromArgs(args); explicit != "" {
		return explicit
	}
	if fromEnv := strings.TrimSpace(os.Getenv(configPathEnv)); fromEnv != "" {
		return fromEnv
	}
	if _, err := os.Stat(defaultConfigFileName); err == nil {
		return defaultConfigFileName
	}
	if executable, err := os.Executable(); err == nil {
		if path := configPathNearExecutable(executable); path != "" {
			return path
		}
	}
	return ""
}

func configPathNearExecutable(executable string) string {
	candidates := []string{executable}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil && resolved != executable {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		beside := filepath.Join(filepath.Dir(candidate), defaultConfigFileName)
		if info, err := os.Stat(beside); err == nil && info.Mode().IsRegular() {
			return beside
		}
	}
	return ""
}

// configPathFromArgs 解析 --config=path 和 --config path 两种写法。
func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			return strings.TrimSpace(value)
		}
		if arg == "--config" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

// loadAppConfig 读取并解析配置文件。文件不存在不算错误——全新部署第一次跑起来
// 就该能进安装向导，而不是先要求写一份 YAML。
func loadAppConfig(path string) (appConfig, error) {
	cfg := appConfig{path: path}
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg.path = ""
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Storage.DebugLogRetentionDays < -1 || cfg.Storage.DebugLogRetentionDays > 36500 ||
		cfg.Storage.LogRetentionDays < -1 || cfg.Storage.LogRetentionDays > 36500 {
		return cfg, fmt.Errorf("storage log retention days must be between -1 and 36500")
	}
	if cfg.Storage.DownloadCacheRetentionDays < -1 || cfg.Storage.DownloadCacheRetentionDays > 36500 {
		return cfg, fmt.Errorf("storage download_cache_retention_days must be between -1 and 36500")
	}
	if cfg.Storage.DownloadCacheMaxMB < 0 || cfg.Storage.DownloadCacheMaxMB > 1<<20 {
		return cfg, fmt.Errorf("storage download_cache_max_mb must be between 0 and 1048576")
	}
	cfg.path = path
	return cfg, nil
}

// decodeSection 把 YAML 里的一段按 JSON tag 解到目标结构体。业务配置的字段名
// 因此和 WebUI 接口的 payload 一致，config.yaml 抄接口文档就能写。
func decodeSection(node yaml.Node, dest any) error {
	if node.IsZero() {
		return nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dest)
}

// botSeedConfig 返回首启播种用的机器人配置。数据库已有配置时这份不会被采用。
func (c appConfig) botSeedConfig(defaultEndpoint string) (assistant.BotConfig, bool, error) {
	base := assistant.DefaultBotConfig()
	base.OneBotReverseWSEndpoint = defaultEndpoint
	if c.Bot.IsZero() {
		return base.WithDefaults(), false, nil
	}
	var payload assistant.ConfigPayload
	if err := decodeSection(c.Bot, &payload); err != nil {
		return assistant.BotConfig{}, false, fmt.Errorf("parse bot section: %w", err)
	}
	// 兜底要在转换前补进 payload：ConfigFromPayload 内部会把空的反连地址补成内置
	// 常量（写死 18080），补在后面就盖不掉了，换了端口的部署会拿到错的默认地址。
	if strings.TrimSpace(payload.OneBotReverseWSEndpoint) == "" {
		payload.OneBotReverseWSEndpoint = defaultEndpoint
	}
	return assistant.ConfigFromPayload(payload, base).WithDefaults(), true, nil
}

// llmSeedConfig 返回首启播种用的提供商配置。
func (c appConfig) llmSeedConfig() (llm.ProviderConfig, bool, error) {
	if c.LLM.IsZero() {
		return llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible}.WithDefaults(), false, nil
	}
	cfg := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible}
	if err := decodeSection(c.LLM, &cfg); err != nil {
		return llm.ProviderConfig{}, false, fmt.Errorf("parse llm section: %w", err)
	}
	return cfg.WithDefaults(), true, nil
}

// boolOr 在配置没写这一项时返回默认值。
func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// stringOr 在配置项为空时返回默认值。
func stringOr(value string, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// defaultOneBotEndpoint 是没写反连地址时的兜底：回连到本进程自己的端口，
// 本地联调时接入端只要指向这个地址就能连上。
func defaultOneBotEndpoint(port string) string {
	return "ws://127.0.0.1:" + port + "/onebot/v11/ws"
}

// trimmedList 去掉列表里的空白项，配置文件里留空行或写个空字符串都不算数。
func trimmedList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// reportSeedOutcome 说明 config.yaml 里的业务段这次到底有没有被采用。
// 数据库已有配置时播种不生效，这件事必须说出来——以前环境变量就是在这里
// 静默失效的,改了配置重启没反应,也没有任何提示。
func reportSeedOutcome(path string, llmSeeded bool, llmSeed llm.ProviderConfig, llmStored llm.ProviderConfig, botSeeded bool, botSeed assistant.BotConfig, botStored assistant.BotConfig) {
	if path == "" {
		return
	}
	if llmSeeded {
		reportSectionOutcome(path, "llm", llmSeed.APIKey == llmStored.APIKey && llmSeed.Model == llmStored.Model && llmSeed.BaseURL == llmStored.BaseURL)
	}
	if botSeeded {
		reportSectionOutcome(path, "bot", botSeed.OneBotAccessToken == botStored.OneBotAccessToken &&
			botSeed.OneBotReverseWSEndpoint == botStored.OneBotReverseWSEndpoint &&
			botSeed.OwnerID == botStored.OwnerID)
	}
}

// reportSectionOutcome 对单个业务段打一行结论。只报字段是否一致，不打印值本身，
// 这两段里有 API Key 和 access token。
func reportSectionOutcome(path string, section string, matches bool) {
	if matches {
		log.Printf("config: %s section in %s is in effect", section, path)
		return
	}
	log.Printf("config: %s section in %s was NOT applied; the stored configuration differs and wins. Edit it in the WebUI, or clear the database to re-seed.", section, path)
}
