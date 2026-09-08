package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestProviderRejectionClassification(t *testing.T) {
	for _, err := range []error{llm.ErrUnverifiedRejection, &llm.ContentBlockedError{Provider: llm.ProviderGemini, Reason: "SAFETY"}, errors.New("403 content_policy_violation")} {
		if shouldFailoverLLMError(err) || shouldRetryTransientLLMError(err) {
			t.Fatalf("rejection retried: %v", err)
		}
	}
	for _, err := range []error{errors.New("403 Forbidden: provider permission denied"), errors.New("429 quota exceeded"), errors.New("503 service unavailable"), errors.New("model_not_found")} {
		if !shouldFailoverLLMError(err) {
			t.Fatalf("technical failure not eligible: %v", err)
		}
	}
	notice := "The prompt could not be submitted. The prompt contains sensitive words that violate Google's [Generative AI Prohibited Use policy](https://policies.google.com/terms/generative-ai/use-policy). Try rephrasing the prompt. If you think this was an error, [send feedback](https://ai.google.dev/gemini-api/docs/troubleshooting)."
	provider := &capturingLLMProvider{reply: notice}
	response, err := generateWithTransientRetry(context.Background(), provider, llm.GenerateRequest{}, true)
	if response != nil || !errors.Is(err, llm.ErrUnverifiedRejection) {
		t.Fatalf("response=%v err=%v", response, err)
	}
	if isContentPolicyRejection(err) {
		t.Fatal("unverified notice treated as confirmed block")
	}
	if text := publicChatErrorMessage(err); strings.Contains(text, "Google") || !strings.Contains(text, "无法确认") {
		t.Fatalf("public notice=%s", text)
	}
}

func TestStreamingRejectionDoesNotRetryAsNonStreaming(t *testing.T) {
	blocked := &llm.ContentBlockedError{Provider: llm.ProviderGemini, Stage: "candidate", Reason: "SAFETY"}
	for _, stream := range []*stubStreamer{
		{streamErr: blocked},
		{events: []llm.ChatEvent{{Type: llm.ChatEventError, Error: blocked.Error(), ErrorCause: blocked}}},
	} {
		response, err := (&streamingLLMProvider{provider: stream}).Generate(context.Background(), llm.GenerateRequest{})
		if response != nil || !errors.Is(err, llm.ErrContentBlocked) || stream.generated {
			t.Fatalf("response=%v err=%v retried=%v", response, err, stream.generated)
		}
	}
}
