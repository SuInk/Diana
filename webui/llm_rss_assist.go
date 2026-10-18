// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/gin-gonic/gin"
)

const rssAssistMaxRunes = 2000

type rssAssistPayload struct {
	Kind      string `json:"kind"`
	Request   string `json:"request"`
	ProfileID string `json:"profile_id,omitempty"`
	Group     string `json:"group,omitempty"`
	Model     string `json:"model,omitempty"`
}

var rssAssistPrompts = map[string]string{
	"regex": `把管理员的需求改写成一个 Go RE2 正则表达式，用于匹配 RSS 条目的标题和正文。表达式应尽量准确、简洁；需要忽略大小写时使用 (?i)。Go RE2 不支持前后查找和反向引用。只输出正则表达式本身，不要代码围栏、引号、解释或前言。`,
	"judge": `把管理员的草稿改写成清楚、可执行的 RSS 判断与回复规则。规则交给模型判断文字、图片、视频画面和音频是否值得通知，应明确满足什么条件、排除什么情况。默认发送命中条目的原文；只有管理员明确提出翻译、总结、改写、提取或输出格式时，才把这项处理要求写进规则，不要自行添加。只输出规则正文，不要前言。`,
	"reply": `把管理员的草稿改写成清楚、简洁的 RSS 回复格式要求。它只控制标题和正文怎样改写，例如翻译语言、摘要长度、语气和结构；原文链接与媒体由程序自动保留。只输出要求正文，不要前言。`,
}

// rssAssist 用当前机器人的对话模型整理订阅规则，只返回草稿，不保存订阅。
func (h *LLMConfigHandler) rssAssist(c *gin.Context) {
	var payload rssAssistPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_rss_assist", err, "", nil)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(payload.Kind))
	prompt := rssAssistPrompts[kind]
	request := strings.TrimSpace(payload.Request)
	if prompt == "" {
		h.writeError(c, http.StatusBadRequest, "llm_rss_assist", fmt.Errorf("不支持的帮写类型"), payload.Model, nil)
		return
	}
	if request == "" {
		h.writeError(c, http.StatusBadRequest, "llm_rss_assist", fmt.Errorf("请先写一句需求"), payload.Model, nil)
		return
	}
	if runes := []rune(request); len(runes) > rssAssistMaxRunes {
		request = string(runes[:rssAssistMaxRunes])
	}

	cfg, err := personaProviderConfig(h.store.Profiles(), personaGeneratePayload{
		ProfileID: payload.ProfileID,
		Group:     payload.Group,
		Model:     payload.Model,
	})
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm_rss_assist", err, payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_rss_assist", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}

	started := time.Now()
	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: request},
	}})
	if err != nil {
		h.writeError(c, statusUpstreamFailed, "llm_rss_assist", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordLLMUsage(c, h.logs, resp.Provider, firstNonEmpty(resp.Model, cfg.Model), resp.Usage, "webui_rss_assist", time.Since(started))
	text := normalizeRSSAssist(resp.Text)
	if text == "" {
		h.writeError(c, statusUpstreamFailed, "llm_rss_assist", fmt.Errorf("模型没有返回可用内容"), cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	if kind == "regex" {
		if _, err := regexp.Compile(text); err != nil {
			h.writeError(c, statusUpstreamFailed, "llm_rss_assist", fmt.Errorf("模型返回的正则无效：%w", err), cfg.Model, llmLogMetadata(cfg, ""))
			return
		}
	}
	recordRequestOperation(c, h.logs, "llm_rss_assist", "RSS 规则帮写成功", resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{"text": text, "model": resp.Model, "provider": resp.Provider})
}

func normalizeRSSAssist(text string) string {
	text = strings.TrimSpace(stripPersonaCodeFence(strings.TrimSpace(text)))
	if len([]rune(text)) > rssAssistMaxRunes {
		text = strings.TrimSpace(string([]rune(text)[:rssAssistMaxRunes]))
	}
	return text
}
