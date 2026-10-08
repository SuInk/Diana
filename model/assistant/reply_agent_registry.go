// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"

	"github.com/SuInk/diana/model/agent"
)

// newReplyAgentRegistry keeps chat and scheduled execution on the same tool,
// plugin override and permission path. Callers own the returned registry.
func (r *Runtime) newReplyAgentRegistry(ctx context.Context, cfg BotConfig, event MessageEvent, relationship RelationshipPolicy, recallSink *recallDisclosureSink) (*agent.ToolRegistry, error) {
	overrides := r.pluginOverridesForEvent(event)
	settingOverrides := r.pluginSettingOverridesForEvent(event)
	imageMode := normalizeImageInputMode(cfg.ImageInputMode)
	imageTextOnly := r.imageDescriptionsInPrompt(cfg)
	fullAgentEnabled := cfg.AgentEnabled
	var agentRegistry *agent.ToolRegistry

	var pluginTools []agent.Tool
	if r.plugins != nil {
		var pluginToolsErr error
		pluginTools, pluginToolsErr = r.plugins.AgentToolsForPlatformWithGroupOverrides(cfg.Platform, overrides, settingOverrides)
		if pluginToolsErr != nil {
			return nil, pluginToolsErr
		}
	}
	for index, tool := range pluginTools {
		pluginTools[index] = capabilityToolForConfig(tool, cfg)
	}
	if r.platformInterfaceEnabled(event) {
		pluginTools = append(pluginTools, newDianaPlatformTool(ctx, r, event))
	}
	if fullAgentEnabled {
		// 因为权限不够而没挂上的工具名。它们不构造、不注册，只是让注册表知道
		// 「有过这个名字，但这次会话没权限」，取不到时才说得出正确的那句话。
		var deniedTools []string
		extraTools := []agent.Tool{
			newDianaChatHistoryTool(r, event).withRecallSink(recallSink),
			newDianaHistoryImagesTool(r, event).withImageInput(imageTextOnly, imageMode),
			newDianaRemoteImageTool(r, event),
			newDianaMCPMediaTool(r, event),
			&dianaTelegramImagesTool{runtime: r, event: event},
			&dianaLocalAttachmentTool{runtime: r, event: event, view: true},
			&dianaLocalAttachmentTool{runtime: r, event: event},
			newDianaSubtaskTool(r, event),
			newDianaRelationshipTool(r, event),
			newDianaNotebookTool(r, event, relationship),
			newDianaVersionTool(r, repositoryDisclosedTo(cfg, relationship.Owner)),
			newDianaImageTool(r, event, relationship),
			newDianaTasksTool(r, event),
			newDianaBotConfigTool(r, event),
			newDianaReplyBlockTool(r, event),
			newDianaReminderTool(r, event),
			newDianaEventTriggerTool(r, event),
			newDianaRenderTool(r, event),
			// 只读、无参数，但仍是主人专属：主机名、磁盘路径、硬件型号
			// 不该对群里所有人可见。靠 allowedAgentToolNames 不收录它来实现。
			newDianaHostStatsTool(r, event),
		}
		if IsOneBotPlatform(r.currentPlatform(event)) {
			extraTools = append(extraTools, newDianaPokeTool(r, event))
		}
		// 视频生成只在模型分配里配了插槽时才挂：没配时模型看得到也只能失败。
		// 权限跟着生图走，没有生图权限的人也拿不到视频。
		if relationship.AllowImageGeneration && r.mediaSlotConfigured(ctx, mediaSlotVideo) {
			extraTools = append(extraTools, newDianaVideoTool(r, event, relationship))
		}
		// 存二进制文件和 write_file 同一档：都是往磁盘上写，跟着「允许写入文件」走。
		// 它不在 allowedAgentToolNames 里，群成员拿不到。
		if cfg.agentFileWriteAllowed() {
			extraTools = append(extraTools, newDianaSaveToWorkspaceTool(r, event))
		}
		// 跨会话发送只在「确实存在另一条会话可发」时才有意义。群里人人可用，
		// 但只能发给当前说话的人；主人在哪都能用，因为只有他能指定别人和群。
		// 私聊里给普通成员挂上它，模型看得到就会去调，然后只能被拒绝，白费一轮。
		if event.Kind == EventKindGroup || relationship.Owner {
			extraTools = append(extraTools, newDianaCrossSessionTool(r, event, relationship.Owner))
		} else {
			deniedTools = append(deniedTools, dianaCrossSessionToolName)
		}
		if supportsOneBotGroupTool(cfg, event) {
			extraTools = append(extraTools, newDianaGroupTool(r, event))
		}
		if r.threadStateStore() != nil {
			extraTools = append(extraTools, newDianaThreadStateTool(r, event))
		}
		// 自述默认关着，开关在机器人配置上：工具和注入层要同时受它约束，否则
		// 模型会写进一个不会被读出来的地方。
		if r.selfNoteEnabled(event) {
			extraTools = append(extraTools, newDianaSelfNoteTool(r, event, relationship))
		}
		// 动态只给主人：发在控制台里的内容不该由群里任何人一句话就能塞进去。
		if relationship.Owner && r.feedStore() != nil {
			extraTools = append(extraTools, newDianaFeedTool(r, event))
		}
		r.mu.RLock()
		memoryAvailable := r.structuredMemory != nil
		r.mu.RUnlock()
		if memoryAvailable {
			extraTools = append(extraTools, &dianaMemoryTool{runtime: r, event: event})
		}
		if r.oneBotRequestStore() != nil && IsOneBotPlatform(r.currentPlatform(event)) && r.platformInterfaceEnabled(event) {
			extraTools = append(extraTools, newDianaOneBotRequestsTool(r, event))
		}
		// 关系图按插件开关走：不是每个群都想让机器人画这个，渲染也要占一次
		// 无头浏览器。插件停用时模型看不到这个工具。
		if _, settings, enabled := r.pluginWithSettingsForEvent(groupRelationsPluginID, event); enabled {
			extraTools = append(extraTools, newDianaGroupRelationsTool(r, event, settings))
		}
		// 中途说一句：先说「我去查」再真的去查，长任务分段报进度。说完这一轮不结束。
		extraTools = append(extraTools, newDianaInterimMessageTool(r, event))
		// 请主人在内置浏览器里亲手做一步（登录、扫码、验证码）。浏览器工具本来就只有
		// 主人能用，这个也只挂给主人；内置浏览器没开时挂上也只能失败，不挂。
		if relationship.Owner {
			if _, ok := r.browserBoxFor(cfg).(browserHandoffRequester); ok {
				extraTools = append(extraTools, newDianaBrowserHandoffTool(r, event, cfg))
			}
		}
		if _, settings, enabled := r.pluginWithSettingsForEvent(stickerPluginID, event); enabled {
			extraTools = append(extraTools, newDianaStickerTool(r, event, settings))
		}
		// VRChat 联动默认关闭；开着时查状态人人可用，操控类工具默认只给主人。
		if pluginValue, settings, enabled := r.pluginWithSettingsForEvent(vrchatPluginID, event); enabled {
			if plugin, ok := pluginValue.(*VRChatPlugin); ok {
				tools, denied := newDianaVRChatTools(plugin, settings, relationship.Owner, r.eventProfileID(event))
				extraTools = append(extraTools, tools...)
				deniedTools = append(deniedTools, denied...)
			}
		}
		// 只有能上传文件的平台才挂：其他平台模型看得到也只能失败。
		if platform := NormalizePlatformID(event.Platform); platform == PlatformTelegram || IsOneBotPlatform(platform) {
			if _, settings, enabled := r.pluginWithSettingsForEvent(fileDeliveryPluginID, event); enabled {
				extraTools = append(extraTools, newDianaFileDeliveryTool(r, event, settings, relationship))
				if settings.Bool(fileDeliverySettingRenderMedia, true) {
					extraTools = append(extraTools, newDianaRenderMediaTool(r, event, settings, relationship))
				}
			}
		}
		// 图片溯源同样按插件开关走：反查要把图片上传给第三方图库，不是每个
		// 群都愿意，插件停用时模型看不到这个工具。
		if pluginValue, settings, enabled := r.pluginWithSettingsForEvent(imageSourcePluginID, event); enabled {
			// 一条线路都没配好时不挂这个工具：模型看得到就会去调，然后只能
			// 回一句「查不了」，白费一轮。
			if plugin, ok := pluginValue.(*ImageSourcePlugin); ok && imageSourceConfigFromSettings(settings).anyProviderUsable() {
				extraTools = append(extraTools, newDianaImageSourceTool(r, event, plugin, settings))
			}
		}
		// AI 图片检测默认只在本地解析元数据，图片不出网；配了 SynthID 检测服务
		// 才会上传。插件停用时模型看不到这个工具。
		if pluginValue, settings, enabled := r.pluginWithSettingsForEvent(aiImageDetectPluginID, event); enabled {
			if plugin, ok := pluginValue.(*AIImageDetectPlugin); ok {
				extraTools = append(extraTools, newDianaAIImageDetectTool(r, event, plugin, settings))
			}
		}
		if pluginValue, settings, enabled := r.pluginWithSettingsForEvent(repositoryPublishPluginID, event); enabled {
			if plugin, ok := pluginValue.(*RepositoryPublishPlugin); ok {
				if relationship.Owner || repositoryPublishEventHasAccess(event, settings) {
					extraTools = append(extraTools, newDianaGitHubTool(r, event, plugin, settings))
				} else {
					// 插件开着、只是这个人这个群不够格。不登记的话模型只会被告知
					// 「不存在」，然后换个名字接着猜。
					deniedTools = append(deniedTools, dianaGitHubToolName)
				}
			}
		}
		// schedule、rss、github 三种订阅合成一个 subscription 工具。github 那种仍然
		// 只挂给主人和仓库管理人员——它不进 backends，kind 枚举里就不会出现，
		// 没权限的人看不见也就不会去调。
		var githubWatch *dianaRepositoryWatchTool
		if pluginValue, watchSettings, enabled := r.pluginWithSettingsForEvent(repositoryWatchPluginID, event); enabled {
			if _, ok := pluginValue.(*RepositoryWatchPlugin); ok {
				_, publishSettings, _ := r.pluginWithSettingsForEvent(repositoryPublishPluginID, event)
				managed := repositoryWatchManagedRepositories(event, publishSettings)
				if relationship.Owner || len(managed) > 0 {
					githubWatch = newDianaRepositoryWatchTool(r, event, relationship.Owner, managed, watchSettings)
				}
			}
		}
		if subscription := newDianaSubscriptionTool(
			subscriptionBackend{
				kind: subscriptionKindSchedule, label: "重复提醒或周期查询",
				operations: []string{"create", "list", "update", "cancel", "delete"},
				delegate:   newDianaScheduleTool(r, event),
			},
			subscriptionBackend{
				kind: subscriptionKindRSS, label: "盯 RSS/Atom 或 X 用户，按 judge_prompt 判断是否通知",
				operations: []string{"create", "list", "update", "cancel", "delete"},
				delegate:   newDianaRSSWatchTool(r, event),
			},
			subscriptionBackend{
				kind: subscriptionKindGitHub, label: "盯 GitHub 仓库动态",
				operations: []string{"create", "list", "update", "cancel", "delete", "run"},
				delegate:   subscriptionGitHubDelegate(githubWatch),
			},
		); subscription != nil {
			extraTools = append(extraTools, subscription)
		}
		// 装包只挂给主人：它会从 npm 注册表拉代码进工作区。安全模式下不构造，
		// 没开文件写入时装了也写不出脚本来用，一并不挂。
		if relationship.Owner && !cfg.agentSafeMode() && cfg.AgentFileWriteEnabled {
			extraTools = append(extraTools, newDianaInstallPackageTool(r, event, cfg))
		}
		// 编码代理只挂给主人：它能在白名单仓库里不受限地跑命令和改代码，
		// 不走 Agent 的命令白名单沙盒。allowedAgentToolNames 不收录它，这里
		// 再按身份筛一次，两道闸都在。
		// 安全模式下连主人也不构造：编码代理在安全模式要关掉的第一类里。
		if pluginValue, settings, enabled := r.pluginWithSettingsForEvent(codingAgentPluginID, event); enabled && relationship.Owner && !cfg.agentSafeMode() {
			if _, ok := pluginValue.(*CodingAgentPlugin); ok {
				extraTools = append(extraTools, newDianaCodingTool(r, event, settings))
			}
		}
		if boolValue(cfg.OwnerLLMConfigEnabled, true) {
			extraTools = append(extraTools, newDianaLLMConfigTool(r, event))
		}
		extraTools = append(extraTools, pluginTools...)
		var err error
		agentRegistry, err = r.newAgentRegistry(ctx, cfg, event, relationship, extraTools...)
		if err != nil {
			return nil, err
		}
		agentRegistry.DenyTools(deniedTools...)
	} else if len(pluginTools) > 0 && relationship.allowsAgentTools() {
		// Plugin-contributed model tools stay usable without granting the local
		// filesystem, shell, browser, skills, or MCP surface behind AgentEnabled.
		agentRegistry = agent.NewToolRegistry(pluginTools...)
		agentRegistry.Retain(r.allowedAgentToolNamesForEvent(event, relationship))
		r.wrapScreenshotTools(agentRegistry, event)
	}
	attachCapabilityRegistry(pluginTools, agentRegistry)
	return agentRegistry, nil
}
