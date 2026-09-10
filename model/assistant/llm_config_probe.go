package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const modelSwitchProbeTimeout = 30 * time.Second
const modelSwitchImageProbeTimeout = 90 * time.Second
const modelSwitchProbeImage = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAXklEQVR4nO3PMQ0AMAzAsPInvYLYYVWKESTzjhsd8KsBrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BrQGtAa0BbQHKU9LC7/CP1AAAAABJRU5ErkJggg=="

// Probe the exact target without runtime routing/failover, before any writes.
// Only synthetic input is sent; probe output never enters the chat or history.
func (r *Runtime) probeModelSwitch(ctx context.Context, registry *llm.ProviderRegistry, providerID string, cfg llm.ProviderConfig, role string) error {
	timeout := modelSwitchProbeTimeout
	if role == llmConfigRoleImage {
		timeout = modelSwitchImageProbeTimeout
		cfg.ImageModel = cfg.Model
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	var client LLMProvider
	if registry != nil && role != llmConfigRoleImage {
		client = llm.RegistryClient{Registry: registry, Selection: normalizeRegistrySelection(registry, providerID, cfg.Model)}
	} else {
		r.mu.RLock()
		factory := r.llmCfgFactory
		r.mu.RUnlock()
		var err error
		if factory != nil {
			client, err = factory(cfg)
		} else {
			client, err = llm.NewClient(cfg)
		}
		if err != nil {
			return err
		}
	}
	if client == nil {
		return fmt.Errorf("目标模型客户端不可用")
	}
	if role == llmConfigRoleImage {
		generator, ok := client.(llm.ImageGenerator)
		if !ok {
			return fmt.Errorf("目标提供商不支持图片生成")
		}
		response, err := generator.GenerateImage(ctx, llm.ImageGenerateRequest{Model: cfg.Model, Prompt: "Generate a plain white image without text.", N: 1})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if response != nil {
			for _, source := range response.Images {
				if strings.TrimSpace(source) != "" {
					return nil
				}
			}
		}
		return fmt.Errorf("测试请求未返回图片")
	}
	message := llm.Message{Role: llm.RoleUser, Content: "Reply with OK."}
	if role == llmConfigRoleVision {
		message.Content = "Describe the supplied image briefly."
		message.Parts = []llm.ContentPart{{Type: llm.ContentPartText, Text: message.Content}, {Type: llm.ContentPartImageURL, ImageURL: modelSwitchProbeImage}}
	}
	response, err := client.Generate(ctx, llm.GenerateRequest{Model: cfg.Model, Messages: []llm.Message{message}, MaxOutputTokens: cfg.MaxOutputTokens, ReasoningEffort: cfg.ReasoningEffort})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if response == nil || strings.TrimSpace(response.Text) == "" {
		return fmt.Errorf("测试请求未返回有效文本")
	}
	return nil
}

func modelSwitchProbeError(cfg llm.ProviderConfig, err error) string {
	message := err.Error()
	if secret := strings.TrimSpace(cfg.APIKey); secret != "" {
		message = strings.ReplaceAll(message, secret, "***")
	}
	return sanitizePublicErrorDetail(message)
}
