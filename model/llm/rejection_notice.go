package llm

import (
	"errors"
	"strings"
)

var ErrUnverifiedRejection = errors.New("llm: unverified upstream rejection notice")

const UnverifiedRejectionNotice = "The prompt could not be submitted. The prompt contains sensitive words that violate Google's Generative AI Prohibited Use policy (https://policies.google.com/terms/generative-ai/use-policy). Try rephrasing the prompt. If you think this was an error, send feedback (https://ai.google.dev/gemini-api/docs/troubleshooting)."

// RejectionNoticeError recognizes a known gateway notice, not arbitrary refusal
// prose. It cannot establish which upstream service made the policy decision.
func RejectionNoticeError(text string) error {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "[Generative AI Prohibited Use policy](https://policies.google.com/terms/generative-ai/use-policy)", "Generative AI Prohibited Use policy (https://policies.google.com/terms/generative-ai/use-policy)")
	text = strings.ReplaceAll(text, "[send feedback](https://ai.google.dev/gemini-api/docs/troubleshooting)", "send feedback (https://ai.google.dev/gemini-api/docs/troubleshooting)")
	if strings.Join(strings.Fields(text), " ") == UnverifiedRejectionNotice {
		return ErrUnverifiedRejection
	}
	return nil
}
