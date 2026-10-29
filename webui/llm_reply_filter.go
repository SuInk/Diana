// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/gin-gonic/gin"
)

const (
	replyFilterAssistMaxRunes = 2000
	replyFilterTestMaxRunes   = 8000
)

const replyFilterAssistPrompt = `把管理员的需求写成一条 Go RE2 正则表达式，用来在机器人回复发出前删掉命中的内容。规则按行匹配（已开启多行模式，^ $ 对应每行行首行尾），命中的部分直接删除，不做替换。表达式要尽量窄，只删需求里说的东西，不要误伤正常句子；不能匹配空文本；Go RE2 不支持前后查找和反向引用。管理员可能附上一段示例回复，用它确认表达式能命中。只输出正则表达式本身，不要代码围栏、引号、解释或前言。`

type replyFilterAssistPayload struct {
	Request   string `json:"request"`
	Sample    string `json:"sample,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Group     string `json:"group,omitempty"`
	Model     string `json:"model,omitempty"`
}

// replyFilterAssist 用对话模型把一句需求写成一条回复过滤正则，只返回草稿。
func (h *LLMConfigHandler) replyFilterAssist(c *gin.Context) {
	var payload replyFilterAssistPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_reply_filter_assist", err, "", nil)
		return
	}
	request := truncateReplyFilterInput(strings.TrimSpace(payload.Request), replyFilterAssistMaxRunes)
	if request == "" {
		h.writeError(c, http.StatusBadRequest, "llm_reply_filter_assist", fmt.Errorf("请先写一句想删掉什么"), payload.Model, nil)
		return
	}
	if sample := truncateReplyFilterInput(strings.TrimSpace(payload.Sample), replyFilterAssistMaxRunes); sample != "" {
		request += "\n\n示例回复：\n" + sample
	}
	cfg, err := personaProviderConfig(h.store.Profiles(), personaGeneratePayload{
		ProfileID: payload.ProfileID,
		Group:     payload.Group,
		Model:     payload.Model,
	})
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm_reply_filter_assist", err, payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_reply_filter_assist", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	started := time.Now()
	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: replyFilterAssistPrompt},
		{Role: llm.RoleUser, Content: request},
	}})
	if err != nil {
		h.writeError(c, statusUpstreamFailed, "llm_reply_filter_assist", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordLLMUsage(c, h.logs, resp.Provider, firstNonEmpty(resp.Model, cfg.Model), resp.Usage, "webui_reply_filter_assist", time.Since(started))
	// 模型偶尔会多给几行，只取第一条非空的。
	rule := ""
	for _, line := range strings.Split(normalizeRSSAssist(resp.Text), "\n") {
		if rule = strings.TrimSpace(line); rule != "" {
			break
		}
	}
	if rule == "" {
		h.writeError(c, statusUpstreamFailed, "llm_reply_filter_assist", fmt.Errorf("模型没有返回可用内容"), cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	if err := assistant.ValidateReplyFilterRules([]string{rule}); err != nil {
		h.writeError(c, statusUpstreamFailed, "llm_reply_filter_assist", fmt.Errorf("模型写的规则不能用：%w", err), cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm_reply_filter_assist", "回复过滤规则帮写成功", resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{"rule": rule, "model": resp.Model, "provider": resp.Provider})
}

type replyFilterTestPayload struct {
	Rules []string `json:"rules"`
	Text  string   `json:"text"`
}

// replyFilterTest 用发送时同一套 Go 正则跑一遍样例，浏览器的正则和 RE2 写法有差异。
func (h *LLMConfigHandler) replyFilterTest(c *gin.Context) {
	var payload replyFilterTestPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "reply_filter_test", err, "", nil)
		return
	}
	rules := make([]string, 0, len(payload.Rules))
	for _, rule := range payload.Rules {
		if rule = strings.TrimSpace(rule); rule != "" {
			rules = append(rules, rule)
		}
	}
	result, steps := assistant.PreviewReplyFilters(rules, truncateReplyFilterInput(payload.Text, replyFilterTestMaxRunes))
	c.JSON(http.StatusOK, gin.H{"result": result, "steps": steps})
}

func truncateReplyFilterInput(text string, limit int) string {
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}
