// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	dianaSubtaskToolName = "diana.subtask"
	// maximumSubtaskCallsPerReply 限制单轮扇出规模。子调用是同步的，放开了会把一次
	// 回复拖成一串串行往返；真正需要更大扇出的活属于后台 PluginTask，不是这里。
	maximumSubtaskCallsPerReply = 4
	// maximumSubtaskMaterialRunes 限制单次传入的素材长度。子代理的价值在于「小上下
	// 文办小事」，素材越界就说明这件事该留在主回复里做。
	maximumSubtaskMaterialRunes = 6000
	subtaskTimeout              = 90 * time.Second
	subtaskAnswerMaxRunes       = 1200
)

// subtaskScope 决定子调用走哪个 LLM 配置档分组。名字用意图而不是分组标识，模型
// 不需要知道部署侧怎么配的。
var subtaskScopes = map[string]string{
	"text":   llm.GroupChat,
	"vision": llm.GroupVision,
}

// dianaSubtaskTool 让主 Agent 把一个自足的小问题交给一次独立的模型调用。
//
// 它的用处是回合内扇出：需要分别归纳几份材料、或者对同一份材料做几种互不相干的
// 判断时，主回复不必把每份材料都拉进自己的上下文。子调用只拿到调用方给的素材，
// 没有系统提示、没有聊天历史、没有工具，所以又小又快。
//
// 它不是「把活外包出去」的通用机制：主回复的人格、关系上下文和工具都在主调用里，
// 子调用只负责把素材压成一句结论。需要长时间跑、需要产出消息的活走 PluginTask
// 异步任务，不走这里。
type dianaSubtaskTool struct {
	runtime *Runtime
	event   MessageEvent

	mu    sync.Mutex
	calls int
}

func newDianaSubtaskTool(runtime *Runtime, event MessageEvent) *dianaSubtaskTool {
	return &dianaSubtaskTool{runtime: runtime, event: event}
}

func (t *dianaSubtaskTool) Name() string {
	return dianaSubtaskToolName
}

func (t *dianaSubtaskTool) Description() string {
	return `把一个自足的小问题交给一次独立的轻量模型调用，用于并行归纳多份材料或对同一份材料做互不相干的判断。子调用只看到你传入的素材，没有聊天历史、长期记忆和工具。自己就能回答、需要聊天上下文、或者需要调用其他工具的问题不要用它。`
}

func (t *dianaSubtaskTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"question", "material"}, map[string]any{
		"question": toolStringParam("要子调用回答的问题，必须自足：不能依赖聊天上下文或未随 material 传入的信息。"),
		"material": toolStringParam("回答该问题所需的全部素材原文。子调用看不到别的东西。"),
		"scope":    toolEnumParam("子调用使用的模型类型。素材是文字用 text；素材里含图片描述之外的视觉判断需求时用 vision。", "text", "vision"),
	})
}

func (t *dianaSubtaskTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana subtask: runtime is not configured")
	}
	question := strings.TrimSpace(configToolString(input, "question"))
	material := strings.TrimSpace(configToolString(input, "material"))
	if question == "" || material == "" {
		return "", fmt.Errorf("diana subtask: question 和 material 都不能为空")
	}
	if runes := []rune(material); len(runes) > maximumSubtaskMaterialRunes {
		return "", fmt.Errorf("diana subtask: material 超过 %d 字，这件事应当留在主回复里处理", maximumSubtaskMaterialRunes)
	}
	group, ok := subtaskScopes[strings.TrimSpace(configToolString(input, "scope"))]
	if !ok {
		group = llm.GroupChat
	}
	if err := t.reserve(); err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, subtaskTimeout)
	defer cancel()
	answer, err := t.runtime.runSubtask(callCtx, group, question, material)
	if err != nil {
		return "", fmt.Errorf("diana subtask: %w", err)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "子调用没有得出结论。", nil
	}
	return truncateRunes(answer, subtaskAnswerMaxRunes), nil
}

func (t *dianaSubtaskTool) reserve() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls >= maximumSubtaskCallsPerReply {
		return fmt.Errorf("diana subtask: 单轮最多 %d 次子调用，请把剩下的判断合并处理", maximumSubtaskCallsPerReply)
	}
	t.calls++
	return nil
}

// runSubtask 执行一次子调用。它复用后台任务那套 subagentLLMSem 并发闸，而不是自建
// 一套：主回复、后台任务和子调用共用同一个「同时能有多少路 LLM 在跑」的额度，
// 免得三套限流各管各的，加起来把供应商打满。
func (r *Runtime) runSubtask(ctx context.Context, group, question, material string) (string, error) {
	ctx = withLLMUsagePurpose(ctx, "subtask")
	sem := r.subagentLLMSem
	if sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	messages := []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: strings.TrimSpace(`你在为另一个助手处理一个被拆出来的小问题。只依据 material 里给出的内容回答 question。

要求：
1. material 是你能看到的全部信息。它没写的一律回答「材料里没有」，不要补充常识、不要推测、不要引用你记得的其他内容。
2. 直接给结论和必要依据，不要复述材料、不要写开场白和总结句。
3. 结论会被另一个助手拿去组织成对话回复，所以只写事实，不要带语气、称呼或表情。
4. 材料自相矛盾或不足以判断时，如实说明矛盾或缺口在哪里。`),
		},
		{
			Role:     llm.RoleUser,
			Content:  "question：" + question + "\n\nmaterial：\n" + material,
			Priority: llm.MessagePriorityCurrent,
		},
	}
	return r.runLLMProviderForGroup(ctx, group, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
}
