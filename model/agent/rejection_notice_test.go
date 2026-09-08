package agent

import (
	"context"
	"errors"
	"github.com/SuInk/diana/model/llm"
	"testing"
)

func TestRunnerDoesNotFinalizeGatewayRejectionAsPlainText(t *testing.T) {
	client := &scriptedClient{responses: []string{"The prompt could not be submitted. The prompt contains sensitive words that violate Google's [Generative AI Prohibited Use policy](https://policies.google.com/terms/generative-ai/use-policy). Try rephrasing the prompt. If you think this was an error, [send feedback](https://ai.google.dev/gemini-api/docs/troubleshooting)."}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir()}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	_, err = runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "summarize"}}})
	if !errors.Is(err, llm.ErrUnverifiedRejection) || len(client.requests) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(client.requests))
	}
}
