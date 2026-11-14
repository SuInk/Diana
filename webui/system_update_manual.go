// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"runtime"
	"strings"
)

// 自更新失败时给用户的兜底命令。这里是这几条命令在程序里唯一的出处，前端只展示
// 后端给的值；README 是它们的发布基线，system_update_manual_test.go 逐字核对两边，
// README 改了这里忘了改会直接测试失败。
const (
	manualInstallShellCommand      = "curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh"
	manualInstallTermuxCommand     = "pkg install -y curl tar coreutils && " + manualInstallShellCommand
	manualInstallPowerShellCommand = "irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex"
	manualInstallDockerCommand     = "curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh"

	dockerImageRepository = "ghcr.io/suink/diana:"
)

// manualUpdateCommand 是一段照着执行就能完成更新的命令，按当前部署方式只给一种。
type manualUpdateCommand struct {
	// Deployment 是 docker 或 host。
	Deployment string `json:"deployment"`
	// Shell 是 sh 或 powershell，前端据此提示在哪个终端里运行。
	Shell   string `json:"shell"`
	Command string `json:"command"`
	// Image 是与当前更新通道一致的 Docker 镜像，只在 Docker 部署时给出。
	Image string `json:"image,omitempty"`
	Note  string `json:"note,omitempty"`
}

// manualUpdate 返回当前进程适用的手动更新命令。源码部署（git）走自己的构建流程，
// 一键安装命令会把它换成 Release 包，所以不给。
func (h *SystemUpdateHandler) manualUpdate(gitDeployment bool) *manualUpdateCommand {
	if gitDeployment && !h.dockerDeployment {
		return nil
	}
	image := ""
	if h.dockerUpdater != nil {
		image = h.dockerUpdater.Image
	}
	return manualUpdateFor(h.dockerDeployment, runtime.GOOS, h.currentPolicy().Channel, image)
}

func manualUpdateFor(docker bool, goos, channel, currentImage string) *manualUpdateCommand {
	if docker {
		image := dockerImageForChannel(channel, currentImage)
		return &manualUpdateCommand{
			Deployment: "docker",
			Shell:      "sh",
			Command:    manualInstallDockerCommand,
			Image:      image,
			Note:       "在部署主机上进入原部署目录（docker-compose.yml 和 .env 所在目录）运行。脚本沿用 .env 里的 DIANA_IMAGE，运行前确认它是 " + image + "，与版本面板选的更新通道一致。",
		}
	}
	command := &manualUpdateCommand{Deployment: "host", Shell: "sh", Command: manualInstallShellCommand}
	switch goos {
	case "windows":
		command.Shell = "powershell"
		command.Command = manualInstallPowerShellCommand
		command.Note = "在这台机器上打开 PowerShell 运行，会沿用原安装目录、配置和数据。"
	case "android":
		command.Command = manualInstallTermuxCommand
		command.Note = "在 Termux 终端里运行，会沿用原安装目录、配置和数据。"
	default:
		command.Note = "在这台机器的终端里运行，会沿用原安装目录、配置和数据；以前用 sudo 装过的，脚本会自动接着用原来的安装。"
	}
	if normalizedChannel(channel) != "release" {
		command.Note += "一键命令安装的是最新正式版。"
	}
	return command
}

// dockerImageForChannel 把更新通道换成对应的滚动镜像标签，沿用当前镜像的完整版/基础版。
func dockerImageForChannel(channel, currentImage string) string {
	tag := "latest"
	switch normalizedChannel(channel) {
	case "beta":
		tag = "beta"
	case "canary":
		tag = "canary"
	}
	if strings.HasPrefix(currentImage, dockerImageRepository) && strings.HasSuffix(currentImage, "-slim") {
		tag += "-slim"
	}
	return dockerImageRepository + tag
}

func normalizedChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "beta":
		return "beta"
	case "canary":
		return "canary"
	default:
		return "release"
	}
}
