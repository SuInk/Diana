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
	images := countBudgetImages(req)
	window, reserve := p.runtime.imageRequestBudget(ctx, p.group, req)
	budget := llm.InputTokenBudget(window, reserve)
	before := llm.PlanInputBudget(req, budget)
	run := inputBudgetRunFromContext(ctx)
	if run == nil {
		run = newInputBudgetRun()
	}
	if !before.OverBudget() && run.empty() {
		return p.provider.Generate(ctx, req)
	}
	// 本轮裁过就每步都跑：这一步装得下也要照搬之前丢过的历史、截过的工具结果，
	// 否则前缀在「裁过」和「没裁」之间来回跳，供应商的前缀缓存每步都断。
	req, trim := pretrimBudgetText(req, budget, run)
	if !before.OverBudget() {
		return p.provider.Generate(ctx, req)
	}
	event := MessageEvent{}
	if usage := llmUsageFromContext(ctx); usage != nil {
		event = usage.event
	}
	textCalls := 0
	if llm.PlanInputBudget(req, budget).OverBudget() {
		timeout := 20 * time.Second
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)/3 < timeout {
			timeout = time.Until(deadline) / 3
		}
		if timeout <= 0 {
			return p.provider.Generate(ctx, req)
		}
		describeCtx, cancel := context.WithTimeout(context.WithValue(ctx, imageBudgetActiveKey{}, true), timeout)
		req = fitBudgetText(describeCtx, req, budget, &textCalls, run, p.runtime.summarizeBudgetText)
		if llm.PlanInputBudget(req, budget).ImageExcess > 0 {
			beforeDescriptions := countBudgetImages(req)
			req = fitImagesWithDescriptions(describeCtx, req, budget, func(callCtx context.Context, source string) (string, error) {
				return p.runtime.budgetImageDescription(callCtx, event, source)
			})
			// 图片描述占文字额度。只有真的换了图才再过一遍文字，以前这第二遍无条件跑，
			// 描述一张没换时它只会把第一遍摘不动的那几条再送去摘一次。
			if countBudgetImages(req) < beforeDescriptions {
				var more budgetPretrimStats
				req, more = pretrimBudgetText(req, budget, run)
				trim = trim.add(more)
				req = fitBudgetText(describeCtx, req, budget, &textCalls, run, p.runtime.summarizeBudgetText)
			}
		}
		req = lowerOverBudgetImageDetail(req, budget)
		cancel()
	}
	retained := countBudgetImages(req)
	after := llm.PlanInputBudget(req, budget)
	// 格式：text/img 是「处理前->处理后/限额」；dropped/clipped 是预裁剪丢掉的历史
	// 条数和截短的工具结果条数；images 是「原有/换成描述/保留」。
	log.Printf("diana input budget: message_id=%s budget=%d text=%d->%d/%d img=%d->%d/%d dropped=%d clipped=%d text_summary_calls=%d images=%d/%d/%d", event.MessageID, budget, before.TextTokens, after.TextTokens, after.TextLimit, before.ImageTokens, after.ImageTokens, after.ImageLimit, trim.Dropped, trim.Clipped, textCalls, images, images-retained, retained)
	if after.OverBudget() {
		// 描述、摘要、缩图都做完了还是超，就丢，不让整轮失败。
		//
		// 以前这里分两种：旧上下文太多就交给供应商裁剪，当前问题本身装不下就报错。
		// 后一种在线上照样会发生——一轮带十几张截图很常见，用户看到的是一句「超出
		// 输入预算，未能发出」，一个字的回答都没有。宁可少看几张图、少带点历史，也要
		// 把这一轮答出来。
		//
		// 先丢图：旧消息里的图最先丢，当前这条的图从最后一张往前丢，每张换成一句
		// 占位，模型知道那里有图没送到，不会假装看过。图丢完还超，剩下的交给供应商
		// 客户端按上下文上限裁剪（applyContextBudget → fitMessagesToTokenBudget）。
		var dropped int
		req, dropped = dropOverBudgetImages(req, budget)
		remaining := llm.PlanInputBudget(req, budget)
		log.Printf("diana input budget: message_id=%s still over budget after compression: dropped_images=%d budget=%d text=%d images=%d other=%d over=%t, deferring remainder to provider trim", event.MessageID, dropped, budget, remaining.TextTokens, remaining.ImageTokens, remaining.OtherTokens, remaining.OverBudget())
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
