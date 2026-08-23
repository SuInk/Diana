// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"strings"
)

// 「不支持自更新」和「已经是最新」在界面上必须长得不一样。两者都表现为「没有可
// 点的更新按钮」，但含义相反：前者是这台机器根本升不了级，需要人工换包；后者是
// 已经在最新版上了。以前两种情况都渲染成「已是最新」，用户只会以为自己升过了，
// 直到某天发现版本号停在几个月前。
//
// 这里把更新器内部的英文原因翻译成用户能照着做的说明。翻译不了的原样带出去，
// 至少比一句「不支持」有用。

// updateUnsupportedReasons 把 ReleasePackageUpdater 的内部原因翻译成人话。
// 键是 model/updater 里 unsupportedWhy 的固定前缀。
var updateUnsupportedReasons = []struct {
	prefix string
	text   string
}{
	{"deployment explicitly disabled package replacement", "当前部署通过 DIANA_RELEASE_UPDATE_ENABLED 关闭了自更新。"},
	{"unsupported operating system", "当前操作系统没有对应的 Release 包，只能手动部署。"},
	{"running executable", "正在运行的可执行文件不是 Release 包里的那个（可能是自行构建或改过名），自更新无法确认要替换谁。"},
	{"frontend directory is outside the package root", "前端目录不在可执行文件所在目录之内，自更新无法整包替换；请用官方 Release 包或安装脚本部署。"},
	{"packaged frontend is missing", "找不到随包分发的前端目录，自更新无法校验完整性。"},
	{"SQLite database is missing", "找不到 SQLite 数据库文件，自更新前无法备份数据。"},
	{"health-check URL", "健康检查地址不可用，自更新无法在切换后确认新版本活着。"},
	{"release package updater is not configured", "当前部署没有启用 Release 自更新组件。"},
}

// describeUpdateUnsupported 返回展示给用户的不支持原因。
func describeUpdateUnsupported(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	for _, item := range updateUnsupportedReasons {
		if strings.HasPrefix(reason, item.prefix) {
			return item.text
		}
	}
	return "当前部署不支持自更新：" + reason
}

// gitUpdateUnsupportedReason 描述 Git 部署下无法自更新的情况。源码部署靠 git
// 拉取更新，没有配置远端就没有更新来源。
const gitUpdateUnsupportedReason = "源码部署没有配置 Git 远端，无法拉取更新；请手动更新源码或改用 Release 包部署。"

// dockerUpdateUnsupportedReason 是容器部署的说明。镜像由外部编排替换，控制台
// 只能提示有新版本。
const dockerUpdateUnsupportedReason = "容器部署由镜像负责升级，控制台只提示新版本；请拉取新镜像后重建容器。"

// updateSupportSummary 汇总一次更新检查里和「能不能升级」有关的结论。
type updateSupportSummary struct {
	Supported bool
	Reason    string
}

// releaseUpdateSupport 根据当前部署形态给出结论。gitAvailable 表示源码部署且
// 配置了远端，packageReady 表示 Release 包模式下本平台的资产齐备，
// gitDeployment 表示这是个能读到 Git 仓库的源码部署（哪怕没配远端）。
func (h *SystemUpdateHandler) releaseUpdateSupport(gitAvailable, packageReady, gitDeployment bool) updateSupportSummary {
	if gitAvailable || packageReady {
		return updateSupportSummary{Supported: true}
	}
	if h.releaseUpdater != nil {
		if reason := describeUpdateUnsupported(h.releaseUpdater.UnsupportedReason()); reason != "" {
			return updateSupportSummary{Reason: reason}
		}
		if h.releaseUpdater.Supported() {
			// 更新器本身可用，缺的是这次 Release 里本平台的资产。
			return updateSupportSummary{Reason: "最新 Release 里没有当前平台的完整包或 SHA-256 清单，暂时无法自动升级。"}
		}
	}
	if gitDeployment {
		return updateSupportSummary{Reason: gitUpdateUnsupportedReason}
	}
	return updateSupportSummary{Reason: dockerUpdateUnsupportedReason}
}
