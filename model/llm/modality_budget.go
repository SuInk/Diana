package llm

import "encoding/json"

// InputBudgetSplit is an estimate, not provider-reported usage. Text and images
// each receive half the available budget and may borrow the other's unused share.
type InputBudgetSplit struct {
	InputBudget int64 `json:"input_budget"`
	TextTokens  int64 `json:"estimated_text_tokens"`
	ImageTokens int64 `json:"estimated_image_tokens"`
	OtherTokens int64 `json:"estimated_other_tokens"`
	TextLimit   int64 `json:"text_limit"`
	ImageLimit  int64 `json:"image_limit"`
	TextExcess  int64 `json:"text_excess"`
	ImageExcess int64 `json:"image_excess"`
}

func (p InputBudgetSplit) OverBudget() bool {
	return p.TextTokens+p.ImageTokens+p.OtherTokens > p.InputBudget
}

func PlanInputBudget(req GenerateRequest, budget int64) InputBudgetSplit {
	p := InputBudgetSplit{InputBudget: budget}
	for _, message := range req.Messages {
		var images, other int64
		for _, part := range message.Parts {
			if part.Type == ContentPartImageURL && part.ImageURL != "" {
				images += estimatedImageTokens(part.Detail)
			}
			if part.Type == ContentPartInputAudio && part.AudioData != "" {
				other += estimatedAudioTokens(part.AudioData)
			}
		}
		p.ImageTokens += images
		p.OtherTokens += other
		p.TextTokens += estimateMessageTokens(message) - images - other
		if len(message.ToolCalls) > 0 {
			if raw, err := json.Marshal(message.ToolCalls); err == nil {
				p.TextTokens += estimateTextTokens(string(raw))
			}
		}
	}
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			p.TextTokens += estimateTextTokens(string(raw))
		}
	}
	available := max(int64(0), budget-p.OtherTokens)
	textShare := available / 2
	imageShare := available - textShare
	p.TextLimit = textShare + max(int64(0), imageShare-p.ImageTokens)
	p.ImageLimit = imageShare + max(int64(0), textShare-p.TextTokens)
	if p.OverBudget() {
		p.TextExcess = max(int64(0), p.TextTokens-p.TextLimit)
		p.ImageExcess = max(int64(0), p.ImageTokens-p.ImageLimit)
	}
	return p
}
