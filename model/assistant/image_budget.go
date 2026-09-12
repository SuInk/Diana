package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

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
	images := 0
	for _, m := range req.Messages {
		for _, part := range m.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL != "" {
				images++
			}
		}
	}
	window, reserve := p.runtime.imageRequestBudget(ctx, p.group, req)
	budget := llm.InputTokenBudget(window, reserve)
	before := llm.PlanInputBudget(req, budget)
	if !before.OverBudget() {
		return p.provider.Generate(ctx, req)
	}
	timeout := 20 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)/3 < timeout {
		timeout = time.Until(deadline) / 3
	}
	if timeout <= 0 {
		return p.provider.Generate(ctx, req)
	}
	describeCtx, cancel := context.WithTimeout(context.WithValue(ctx, imageBudgetActiveKey{}, true), timeout)
	event := MessageEvent{}
	if usage := llmUsageFromContext(ctx); usage != nil {
		event = usage.event
	}
	textCalls := 0
	req = fitBudgetText(describeCtx, req, budget, &textCalls, p.runtime.summarizeBudgetText)
	if llm.PlanInputBudget(req, budget).ImageExcess > 0 {
		req = fitImagesWithDescriptions(describeCtx, req, budget, func(callCtx context.Context, source string) (string, error) {
			return p.runtime.budgetImageDescription(callCtx, event, source)
		})
	}
	// Image descriptions consume text quota; account for them before proceeding.
	req = fitBudgetText(describeCtx, req, budget, &textCalls, p.runtime.summarizeBudgetText)
	req = lowerOverBudgetImageDetail(req, budget)
	cancel()
	retained := 0
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL != "" {
				retained++
			}
		}
	}
	after := llm.PlanInputBudget(req, budget)
	log.Printf("diana input budget: message_id=%s input_budget=%d text_share_percent=50 estimated_text_before=%d estimated_images_before=%d estimated_text_after=%d estimated_images_after=%d text_limit=%d image_limit=%d text_summary_calls=%d original_images=%d described_images=%d retained_images=%d", event.MessageID, budget, before.TextTokens, before.ImageTokens, after.TextTokens, after.ImageTokens, after.TextLimit, after.ImageLimit, textCalls, images, images-retained, retained)
	if after.OverBudget() {
		// 压不进预算，先分清是「旧上下文太多」还是「这一轮本身就装不下」。
		//
		// 前者不该让整轮失败。线上抓到的多是差几十个 token：一次是限额 118656、
		// 压完 118710，只超 54，用户却什么也收不到。摘要最多两次、每次还可能因为
		// 略长被整条丢弃，凑不出那几十个 token 太容易了。供应商客户端发请求前本来
		// 就会按自己的上下文上限裁剪（applyContextBudget → fitMessagesToTokenBudget，
		// 四个 provider 都走这一步），丢的是最旧的历史，当前问题有优先级保护，
		// 交给那一层远好过整轮报错。
		//
		// 后者仍旧要失败。裁剪保不住的东西只剩当前问题本身，放行等于把用户的问题
		// 截断了再发出去，答出来的东西还不如不答。
		if llm.PlanInputBudget(budgetProtectedRequest(req), budget).OverBudget() {
			return nil, fmt.Errorf("diana: 无法在输入预算内保留本轮内容：预算 %d，估算文字 %d、图片 %d、其他 %d；当前问题本身就超出预算，未截断发出", budget, after.TextTokens, after.ImageTokens, after.OtherTokens)
		}
		log.Printf("diana input budget: message_id=%s compression fell short, deferring to provider trim: budget=%d text=%d images=%d other=%d", event.MessageID, budget, after.TextTokens, after.ImageTokens, after.OtherTokens)
	}
	return p.provider.Generate(ctx, req)
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
		for i, profile := range profiles {
			cfg := profile.Config
			if req.Model != "" {
				cfg.Model = req.Model
			}
			candidate := cfg.MaxContextTokensWithDefault()
			if i == 0 || candidate < window {
				window = candidate
			}
			output := cfg.MaxOutputTokens
			if output <= 0 {
				output = llm.DefaultMaxOutputTokens
			}
			if i == 0 || output > reserve {
				reserve = output
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
			needed -= max(1, cost-int64(2*recallImageDescriptionMaxRunes+128))
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
