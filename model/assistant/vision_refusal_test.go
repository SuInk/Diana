// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestVisionDescriptionRefusedMatchesProductionRefusals 用生产库里实际被缓存下来的
// 拒答原文兜底：这些正是 2026-09-21 让 Diana 坚称「图片又没传过来」的那批。
func TestVisionDescriptionRefusedMatchesProductionRefusals(t *testing.T) {
	refusals := []string{
		"未收到图片内容，无法生成描述。请重新发送需要记录的图片，我再基于画面中清晰可辨的元素输出客观中文描述。",
		"未收到任何图片内容：当前消息中没有附带图片、图片链接或可解析的图像数据，因此无法生成描述。",
		"没有收到图片，请重新发送。",
		"抱歉，我看不到图片，无法描述内容。",
		"当前请求未提供图片，无法进行识别。",
		"I don't see any image in this message. Please attach one and I'll describe it.",
		"No image was provided, so I cannot generate a description.",
		"Sorry, I'm unable to view the image you mentioned.",
	}
	for _, refusal := range refusals {
		if !VisionDescriptionRefused(refusal) {
			t.Errorf("expected refusal to be detected: %q", refusal)
		}
	}
}

// TestVisionDescriptionRefusedKeepsRealDescriptions 防止误杀：真正的描述里出现「未收到」
// 一类字眼，通常是在转述聊天截图里的原文，不能因此把整条描述丢掉。
func TestVisionDescriptionRefusedKeepsRealDescriptions(t *testing.T) {
	descriptions := []string{
		"一张手机群聊界面截图，顶部状态栏显示时间1:06，电量64%。聊天记录里有人说「图片未收到，麻烦重发」，下方是一张夜晚城市街景。",
		"一张浅色聊天界面的截图。标题为「那个对话运行着运行着就变成了英文」，顶部提示条写着「已拦截一条删除命令 —— 未执行。」",
		"图中是一只白猫坐在窗台上，窗外下雨，玻璃上有水痕。",
		"看不到图中右下角的小字，其余部分是一张深色仪表盘截图。",
		"图中不存在可辨认的人物，画面只有一块白板和几支马克笔。",
		"未找到图里提到的那一行报错，截图只到日志的第 12 行。",
		"A screenshot of a terminal showing: no image found in cache, falling back to download.",
		"",
		"   ",
	}
	for _, description := range descriptions {
		if VisionDescriptionRefused(description) {
			t.Errorf("expected description to be kept: %q", description)
		}
	}
}

// TestDescribeRecallImageRejectsRefusal 覆盖真正的防护点：拒答必须以错误返回，调用方
// 才不会把它当成描述缓存起来。
func TestDescribeRecallImageRejectsRefusal(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "vision", Group: llm.GroupVision, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "blind"}},
	}}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
		return fixedTextLLMProvider{text: "未收到图片内容，无法生成描述。请重新发送需要记录的图片。"}, nil
	})
	path, _ := writeRecallImageFixture(t)
	description, err := runtime.describeRecallImage(context.Background(), MessageEvent{}, path)
	if err == nil {
		t.Fatalf("refusal was returned as a description: %q", description)
	}
	if description != "" {
		t.Fatalf("description = %q, want empty", description)
	}
}

// fixedTextLLMProvider 原样返回一段正文，用来模拟「模型拿不到图，但请求成功」。
type fixedTextLLMProvider struct{ text string }

func (p fixedTextLLMProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "blind", Text: p.text}, nil
}
