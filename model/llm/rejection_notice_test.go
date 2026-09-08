package llm

import (
	"errors"
	"testing"
)

const testRejectionNotice = "The prompt could not be submitted. The prompt contains sensitive words that violate Google's [Generative AI Prohibited Use policy](https://policies.google.com/terms/generative-ai/use-policy). Try rephrasing the prompt. If you think this was an error, [send feedback](https://ai.google.dev/gemini-api/docs/troubleshooting)."

func TestRejectionNoticeRequiresWholeKnownNotice(t *testing.T) {
	if !errors.Is(RejectionNoticeError(testRejectionNotice), ErrUnverifiedRejection) {
		t.Fatal("known notice was accepted")
	}
	for _, text := range []string{"The prompt could not be submitted.", "Please explain: " + testRejectionNotice, "I cannot help with that request.", "{\"content\":\"content_filter\"}", "A service can return content policy violation."} {
		if RejectionNoticeError(text) != nil {
			t.Fatalf("ordinary text misclassified: %q", text)
		}
	}
}
