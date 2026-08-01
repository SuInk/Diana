package assistant

import (
	"fmt"
	"strings"
)

const (
	PlatformNapCat    = "napcat"
	PlatformLagrange  = "lagrange"
	PlatformGoCQHTTP  = "go-cqhttp"
	ProtocolOneBotV11 = "onebot-v11-reverse-ws"
)

// PlatformDefinition 描述一个机器人接入平台及其使用的协议适配器。
type PlatformDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Description string `json:"description,omitempty"`
}

var supportedPlatforms = []PlatformDefinition{
	{ID: PlatformNapCat, Name: "NapCat", Protocol: ProtocolOneBotV11, Description: "推荐的 QQNT OneBot 实现"},
	{ID: PlatformLagrange, Name: "Lagrange.Core", Protocol: ProtocolOneBotV11, Description: "跨平台 OneBot 实现"},
	{ID: PlatformGoCQHTTP, Name: "go-cqhttp", Protocol: ProtocolOneBotV11, Description: "兼容既有 go-cqhttp 部署"},
}

// SupportedPlatforms 返回平台注册表副本。
func SupportedPlatforms() []PlatformDefinition {
	return append([]PlatformDefinition(nil), supportedPlatforms...)
}

// NormalizePlatformID 把旧版展示名称迁移为稳定的平台 ID。
func NormalizePlatformID(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "napcat", "napcat / onebot v11", "onebot v11":
		return PlatformNapCat
	case "lagrange", "lagrange.core", "lagrange core":
		return PlatformLagrange
	case "go-cqhttp", "gocqhttp", "go cqhttp":
		return PlatformGoCQHTTP
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// PlatformByID 查找平台定义。
func PlatformByID(id string) (PlatformDefinition, bool) {
	id = NormalizePlatformID(id)
	for _, platform := range supportedPlatforms {
		if platform.ID == id {
			return platform, true
		}
	}
	return PlatformDefinition{}, false
}

// ValidatePlatform 校验平台是否有可用适配器。
func ValidatePlatform(id string) error {
	if _, ok := PlatformByID(id); !ok {
		return fmt.Errorf("qqbot: unsupported platform %q", id)
	}
	return nil
}
