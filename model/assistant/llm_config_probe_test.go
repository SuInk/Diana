package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type modelSwitchTestClient struct{ LLMProvider }

func TestModelSwitchVisionProbeImageIsValid(t *testing.T) {
	raw := strings.TrimPrefix(modelSwitchProbeImage, "data:image/png;base64,")
	if _, err := png.Decode(base64.NewDecoder(base64.StdEncoding, strings.NewReader(raw))); err != nil {
		t.Fatalf("invalid vision probe image: %v", err)
	}
}

func (*modelSwitchTestClient) GenerateImage(context.Context, llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error) {
	return &llm.ImageGenerateResponse{Images: []string{modelSwitchProbeImage}}, nil
}

// Older configuration tests stubbed only model listing. Keep their probes local.
func newTestLLMConfigTool(r *Runtime, event MessageEvent) *dianaLLMConfigTool {
	r.mu.RLock()
	hasFactory := r.llmCfgFactory != nil
	r.mu.RUnlock()
	if !hasFactory {
		r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
			return &modelSwitchTestClient{LLMProvider: &privacyRequestProvider{reply: "OK"}}, nil
		})
	}
	return newDianaLLMConfigTool(r, event)
}

type modelSwitchProbeClient struct {
	generate func(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error)
	image    func(context.Context, llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error)
}

func (p *modelSwitchProbeClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return p.generate(ctx, req)
}
func (p *modelSwitchProbeClient) GenerateImage(ctx context.Context, req llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error) {
	return p.image(ctx, req)
}

func TestModelSwitchProbesExactTargetBeforeSaving(t *testing.T) {
	for _, role := range []string{"chat", "intent", "vision", "image"} {
		t.Run(role, func(t *testing.T) {
			r, saver, _, event := modelSwitchTestRuntime(t)
			calls := 0
			check := func(ctx context.Context, model string) {
				t.Helper()
				calls++
				if saver.calls != 0 || r.effectiveConfigForEvent(event).ModelRoles[role].ProfileID != "one" {
					t.Fatal("binding changed before probe completed")
				}
				if model != "other" {
					t.Fatalf("probed wrong model %q", model)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > modelSwitchImageProbeTimeout {
					t.Fatal("probe has no bounded timeout")
				}
			}
			r.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
				if cfg.BaseURL != "https://two.invalid" || cfg.APIKey != "secret-two" || cfg.Model != "other" {
					t.Fatal("probe used a different provider or model")
				}
				return &modelSwitchProbeClient{
					generate: func(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
						check(ctx, req.Model)
						if len(req.Messages) != 1 || len(req.Tools) != 0 || strings.Contains(req.Messages[0].Content, event.UserID) {
							t.Fatal("probe included conversation or tools")
						}
						if role == "vision" && (len(req.Messages[0].Parts) != 2 || req.Messages[0].Parts[1].ImageURL != modelSwitchProbeImage) {
							t.Fatal("vision model not tested with an image")
						}
						return &llm.GenerateResponse{Text: "OK"}, nil
					},
					image: func(ctx context.Context, req llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error) {
						check(ctx, req.Model)
						if role != "image" || cfg.ImageModel != "other" || req.N != 1 {
							t.Fatal("wrong image probe")
						}
						return &llm.ImageGenerateResponse{Images: []string{modelSwitchProbeImage}}, nil
					},
				}, nil
			})
			output, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other", "role": role})
			if err != nil || calls != 1 || saver.calls != 1 || !strings.Contains(output, `"tested":true`) {
				t.Fatalf("calls=%d saves=%d output=%s err=%v", calls, saver.calls, output, err)
			}
		})
	}
}

func TestModelSwitchProbeFailureDoesNotChangeBinding(t *testing.T) {
	for _, kind := range []string{"upstream", "empty", "nil", "cancelled", "timeout", "empty_image", "factory"} {
		t.Run(kind, func(t *testing.T) {
			r, saver, _, event := modelSwitchTestRuntime(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
				if kind == "factory" {
					return nil, errors.New("client creation failed")
				}
				return &modelSwitchProbeClient{
					generate: func(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
						switch kind {
						case "upstream":
							return nil, errors.New("403 rejected secret-two https://two.invalid/path")
						case "nil":
							return nil, nil
						case "timeout":
							return nil, context.DeadlineExceeded
						case "cancelled":
							cancel()
							return &llm.GenerateResponse{Text: "OK"}, nil
						default:
							return &llm.GenerateResponse{}, nil
						}
					},
					image: func(context.Context, llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error) {
						return &llm.ImageGenerateResponse{Images: []string{" "}}, nil
					},
				}, nil
			})
			role := "chat"
			if kind == "empty_image" {
				role = "image"
			}
			_, err := newDianaLLMConfigTool(r, event).Run(ctx, map[string]any{"provider_id": "two", "model": "other", "role": role})
			if err == nil || !strings.Contains(err.Error(), "未修改模型分配") {
				t.Fatalf("unexpected result: %v", err)
			}
			if strings.Contains(err.Error(), "secret-two") || strings.Contains(err.Error(), "two.invalid") {
				t.Fatal("probe error leaked credentials")
			}
			if saver.calls != 0 || saver.configs["b"].ModelRoles[role].ProfileID != "one" || r.effectiveConfigForEvent(event).ModelRoles[role].ProfileID != "one" {
				t.Fatal("failed probe changed model")
			}
		})
	}
}

func TestModelSwitchProbeDoesNotUseFallbackProvider(t *testing.T) {
	r, saver, _, event := modelSwitchTestRuntime(t)
	registry := llm.NewProviderRegistry()
	good := &retryRegistryAdapter{succeedAt: 1}
	bad := &retryRegistryAdapter{err: errors.New("target unavailable")}
	for id, adapter := range map[string]*retryRegistryAdapter{"one": good, "two": bad} {
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, adapter); err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"shared", "other"} {
			if err := registry.RegisterModel(llm.ModelDefinition{ID: id + ":" + model, ProviderID: id, ModelID: model}); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.SetLLMProviderRegistry(registry)
	_, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"})
	if err == nil || bad.calls != 1 || good.calls != 0 || saver.calls != 0 {
		t.Fatalf("bad=%d good=%d saves=%d err=%v", bad.calls, good.calls, saver.calls, err)
	}
}

func TestListedModelRejectedByRealHTTPProbeIsNotSaved(t *testing.T) {
	r, saver, store, event := modelSwitchTestRuntime(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		var body struct{ Model string }
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "other" {
			t.Errorf("probe used model=%q", body.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"model not provisioned"}}`))
	}))
	defer server.Close()
	store.set.Profiles[1].Config.BaseURL = server.URL + "/v1"
	r.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) { return llm.NewClient(cfg) })
	_, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"})
	if err == nil || calls == 0 || saver.calls != 0 || r.effectiveConfigForEvent(event).ModelRoles["chat"].Model != "shared" {
		t.Fatalf("calls=%d saves=%d err=%v", calls, saver.calls, err)
	}
}

func TestModelSwitchListAndUnauthorizedRequestsDoNotProbe(t *testing.T) {
	r, saver, _, event := modelSwitchTestRuntime(t)
	probes := 0
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { probes++; return nil, errors.New("unexpected probe") })
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "list"}); err != nil {
		t.Fatal(err)
	}
	event.UserID = "not-owner"
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"}); err == nil {
		t.Fatal("non-owner accepted")
	}
	if probes != 0 || saver.calls != 0 {
		t.Fatal("read-only or unauthorized call triggered probe/write")
	}
}
