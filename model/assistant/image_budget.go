package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

type imageBudgetActiveKey struct{}

func (r *Runtime) withImageBudgetRun(group string, run llmProviderRunFunc) llmProviderRunFunc {
	return func(provider LLMProvider) (string, error) {
		return run(&imageBudgetProvider{runtime: r, provider: provider, group: group})
	}
}

type imageBudgetProvider struct {
	runtime  *Runtime
	provider LLMProvider
	group    string
}

func (p *imageBudgetProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if ctx.Value(imageBudgetActiveKey{}) != nil {
		return p.provider.Generate(ctx, req)
	}
	images := countBudgetImages(req)
	window, reserve := p.runtime.imageRequestBudget(ctx, p.group, req)
	budget := llm.InputTokenBudget(window, reserve)
	before := llm.PlanInputBudget(req, budget)
	// 只处理图片：文字一律原样发送，超窗交给上层压缩（context_compaction.go）。
	// 图片超出时先换成描述、再缩图，实在装不下才换成占位，模型知道那里有图没送到。
	if before.ImageExcess <= 0 {
		return p.provider.Generate(ctx, req)
	}
	event := MessageEvent{}
	if usage := llmUsageFromContext(ctx); usage != nil {
		event = usage.event
	}
	describeCtx := context.WithValue(ctx, imageBudgetActiveKey{}, true)
	req = fitImagesWithDescriptions(describeCtx, req, budget, func(callCtx context.Context, source string) (string, error) {
		return p.runtime.budgetImageDescription(callCtx, event, source)
	})
	req = lowerOverBudgetImageDetail(req, budget)
	retained := countBudgetImages(req)
	after := llm.PlanInputBudget(req, budget)
	log.Printf("diana input budget: message_id=%s budget=%d img=%d->%d/%d images=%d/%d/%d", event.MessageID, budget, before.ImageTokens, after.ImageTokens, after.ImageLimit, images, images-retained, retained)
	if after.ImageExcess > 0 {
		var dropped int
		req, dropped = dropOverBudgetImages(req, budget)
		log.Printf("diana input budget: message_id=%s images still over budget, replaced %d with placeholders", event.MessageID, dropped)
	}
	return p.provider.Generate(ctx, req)
}

func countBudgetImages(req llm.GenerateRequest) int {
	count := 0
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL != "" {
				count++
			}
		}
	}
	return count
}

func (r *Runtime) imageRequestBudget(ctx context.Context, group string, req llm.GenerateRequest) (int64, int64) {
	window, reserve := int64(llm.DefaultContextWindowTokens), int64(llm.DefaultMaxOutputTokens)
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store != nil {
		set := store.Profiles().WithDefaults()
		profiles, _ := r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, group, r.modelRolesForContext(ctx))
		if len(profiles) == 0 {
			profiles = llmProfilesInGroup(set, group)
		}
		if len(profiles) == 0 {
			if first, ok := set.FirstProfile(); ok {
				profiles = []llm.Profile{first}
			}
		}
		// 只按首选模型算，不取整条降级链里最小的窗口。以前取最小值：聊天配了 32k 的
		// 本地备用后，发给 12.8 万窗口主模型的请求也按 32k 卡。真降级到小窗口模型时，
		// 它自己的客户端按自己的上限校验，超了报上下文超限。
		if len(profiles) > 0 {
			cfg := profiles[0].Config
			if req.Model != "" {
				cfg.Model = req.Model
			}
			window = cfg.MaxContextTokensWithDefault()
			if cfg.MaxOutputTokens > 0 {
				reserve = cfg.MaxOutputTokens
			}
		}
	}
	if req.MaxOutputTokens > 0 {
		reserve = req.MaxOutputTokens
	}
	for _, cap := range []int64{req.MaxContextTokens, contextBudgetCapFromContext(ctx)} {
		if cap > 0 && cap < window {
			window = cap
		}
	}
	return window, reserve
}

func imageRequestTokens(req llm.GenerateRequest) int64 {
	plan := llm.PlanInputBudget(req, 0)
	return plan.TextTokens + plan.ImageTokens + plan.OtherTokens
}

func imageBudgetExceeded(req llm.GenerateRequest, budget int64) bool {
	plan := llm.PlanInputBudget(req, budget)
	return plan.OverBudget() && plan.ImageExcess > 0
}

type imageBudgetPosition struct {
	message, part, number int
	source                string
}

// Replace older attachments first, in place, keeping at least the newest image.
// Failed descriptions leave their original attachment intact.
func fitImagesWithDescriptions(ctx context.Context, req llm.GenerateRequest, budget int64, describe func(context.Context, string) (string, error)) llm.GenerateRequest {
	if !imageBudgetExceeded(req, budget) {
		return req
	}
	var positions []imageBudgetPosition
	for mi, m := range req.Messages {
		number := 0
		for pi, p := range m.Parts {
			if p.Type == llm.ContentPartImageURL && p.ImageURL != "" {
				number++
				positions = append(positions, imageBudgetPosition{mi, pi, number, p.ImageURL})
			}
		}
	}
	if len(positions) < 2 {
		return req
	}
	req.Messages = append([]llm.Message(nil), req.Messages...)
	for i := range req.Messages {
		req.Messages[i].Parts = append([]llm.ContentPart(nil), req.Messages[i].Parts...)
	}
	// Identical attachments share a description within this request, even without a store.
	cache := map[string]string{}
	for offset := 0; offset < len(positions)-1 && imageBudgetExceeded(req, budget) && ctx.Err() == nil; {
		end := offset
		needed := imageRequestTokens(req) - budget
		for end < min(offset+recallImageDescriptionConcurrency, len(positions)-1) && needed > 0 {
			pos := positions[end]
			cost := llm.EstimateMessageTokens(llm.Message{Parts: []llm.ContentPart{req.Messages[pos.message].Parts[pos.part]}})
			needed -= max(1, cost-int64(2*recallImageDescriptionEstimateRunes+128))
			end++
		}
		batch := positions[offset:end]
		results := make([]string, len(batch))
		var wg sync.WaitGroup
		pending := map[string]int{}
		for i, pos := range batch {
			if text, ok := cache[pos.source]; ok {
				results[i] = text
				continue
			}
			if _, ok := pending[pos.source]; ok {
				continue
			}
			pending[pos.source] = i
			wg.Add(1)
			go func(i int, source string) {
				defer wg.Done()
				defer recoverGoroutinePanic("image budget description")
				text, err := describe(ctx, source)
				if err == nil {
					results[i] = strings.TrimSpace(text)
				}
			}(i, pos.source)
		}
		wg.Wait()
		for source, i := range pending {
			cache[source] = results[i]
		}
		for _, pos := range batch {
			if !imageBudgetExceeded(req, budget) {
				break
			}
			description := cache[pos.source]
			if description == "" {
				continue
			}
			message := &req.Messages[pos.message]
			replacement := llm.ContentPart{Type: llm.ContentPartText, Text: fmt.Sprintf("【图片%d的文字描述，替代该原图；由视觉模型生成，细节可能不完整】%s", pos.number, description)}
			before := llm.EstimateMessageTokens(*message)
			original := message.Parts[pos.part]
			// Materialize Content before introducing the first text part; adapters otherwise ignore it.
			hasText := false
			for _, part := range message.Parts {
				hasText = hasText || part.Type == llm.ContentPartText && strings.TrimSpace(part.Text) != ""
			}
			if !hasText && strings.TrimSpace(message.Content) != "" {
				replacement.Text = message.Content + "\n" + replacement.Text
			}
			message.Parts[pos.part] = replacement
			if llm.EstimateMessageTokens(*message) >= before {
				message.Parts[pos.part] = original
			}
		}
		offset = end
	}
	return req
}

func (r *Runtime) budgetImageDescription(ctx context.Context, event MessageEvent, source string) (string, error) {
	ready := llmReadyImageURLs(ctx, []string{source})
	if len(ready) == 0 {
		return "", fmt.Errorf("image unavailable")
	}
	body, _, err := decodeInlineHistoryImage(ready[0])
	if err != nil {
		return "", err
	}
	hash := imageBytesSHA256(body)
	store := r.recallImageDescriptionStore()
	if store != nil {
		record, found, err := store.GetImageDescription(ctx, hash)
		if err == nil && found && strings.TrimSpace(record.Description) != "" {
			return record.Description, nil
		}
	}
	description, err := r.describeRecallImage(ctx, event, ready[0])
	if err != nil {
		return "", err
	}
	if store != nil {
		_ = store.SaveImageDescription(ctx, ImageDescriptionRecord{ContentSHA256: hash, Description: description, SourceSession: sessionKey(event), SourceMessageID: event.MessageID, Source: "vision", Version: recallImageDescriptionVersion})
	}
	return description, nil
}
