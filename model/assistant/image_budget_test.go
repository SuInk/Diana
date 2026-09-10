package assistant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func imageBudgetRequest(count int) llm.GenerateRequest {
	message := llm.Message{Role: llm.RoleUser, Content: "请比较这些图片", Priority: llm.MessagePriorityCurrent}
	message.Parts = append(message.Parts, llm.ContentPart{Type: llm.ContentPartText, Text: message.Content})
	for i := 1; i <= count; i++ {
		message.Parts = append(message.Parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: fmt.Sprintf("image-%d", i), Detail: "high"})
	}
	return llm.GenerateRequest{Messages: []llm.Message{message}}
}

func TestImageBudgetReplacesOnlyOverflowAndKeepsOrder(t *testing.T) {
	req := imageBudgetRequest(18)
	budget := llm.InputTokenBudget(128000, 1024)
	got := fitImagesWithDescriptions(context.Background(), req, budget, func(_ context.Context, source string) (string, error) { return "描述对应" + source, nil })
	if imageRequestTokens(got) > budget {
		t.Fatal("request still exceeds budget")
	}
	images, descriptions := 0, 0
	for i, part := range got.Messages[0].Parts[1:] {
		source := fmt.Sprintf("image-%d", i+1)
		if part.Type == llm.ContentPartImageURL {
			images++
			if part.ImageURL != source {
				t.Fatal("image order changed")
			}
		} else {
			descriptions++
			if !strings.Contains(part.Text, fmt.Sprintf("图片%d的文字描述", i+1)) || !strings.Contains(part.Text, "描述对应"+source) {
				t.Fatal("description mismatched")
			}
		}
	}
	if images != 15 || descriptions != 3 {
		t.Fatalf("images=%d descriptions=%d", images, descriptions)
	}
	if got.Messages[0].Parts[18].ImageURL != "image-18" {
		t.Fatal("newest image was replaced")
	}
	if !reflect.DeepEqual(req, imageBudgetRequest(18)) {
		t.Fatal("caller request mutated")
	}
}

func TestImageBudgetNoDescriptionWithoutOverflow(t *testing.T) {
	req := imageBudgetRequest(2)
	got := fitImagesWithDescriptions(context.Background(), req, 128000, func(context.Context, string) (string, error) { t.Fatal("unnecessary description call"); return "", nil })
	if !reflect.DeepEqual(got, req) {
		t.Fatal("fitting request changed")
	}
}

func TestImageBudgetNumbersStayWithinSourceMessage(t *testing.T) {
	first, second := imageBudgetRequest(1), imageBudgetRequest(2)
	first.Messages = append(first.Messages, second.Messages...)
	got := fitImagesWithDescriptions(context.Background(), first, 9000, func(context.Context, string) (string, error) { return "图片描述", nil })
	if !strings.Contains(got.Messages[0].Parts[1].Text, "图片1的文字描述") || !strings.Contains(got.Messages[1].Parts[1].Text, "图片1的文字描述") {
		t.Fatal("attachment numbering crossed message boundaries")
	}
}

func TestImageBudgetFailuresKeepOriginals(t *testing.T) {
	req := imageBudgetRequest(5)
	for _, empty := range []bool{true, false} {
		got := fitImagesWithDescriptions(context.Background(), req, 9000, func(context.Context, string) (string, error) {
			if empty {
				return "", nil
			}
			return "", errors.New("vision unavailable")
		})
		if !reflect.DeepEqual(got, req) {
			t.Fatal("failed description lost an original")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := fitImagesWithDescriptions(ctx, req, 9000, func(context.Context, string) (string, error) { t.Fatal("call after cancellation"); return "", nil })
	if !reflect.DeepEqual(got, req) {
		t.Fatal("canceled request changed")
	}
}

func TestImageBudgetKeepsContentAndDeduplicatesDescriptions(t *testing.T) {
	req := imageBudgetRequest(4)
	req.Messages[0].Parts = req.Messages[0].Parts[1:]
	for i := range req.Messages[0].Parts {
		req.Messages[0].Parts[i].ImageURL = "same-image"
	}
	var calls atomic.Int32
	got := fitImagesWithDescriptions(context.Background(), req, 9000, func(context.Context, string) (string, error) { calls.Add(1); return "同一张图的描述", nil })
	if calls.Load() != 1 {
		t.Fatalf("description calls=%d", calls.Load())
	}
	if !strings.Contains(got.Messages[0].Parts[0].Text, req.Messages[0].Content) {
		t.Fatal("text-only Content disappeared after replacement")
	}
	if imageRequestTokens(got) > 9000 {
		t.Fatal("duplicate replacements did not fit")
	}
}

func TestImageBudgetUsesPersistentVisionCache(t *testing.T) {
	source := imageSourceTestDataURL
	data, _, err := decodeInlineHistoryImage(source)
	if err != nil {
		t.Fatal(err)
	}
	store := newRecallImageTestStore()
	store.descriptions[imageBytesSHA256(data)] = ImageDescriptionRecord{Description: "图中的表格：成功率 63%"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	got, err := runtime.budgetImageDescription(context.Background(), MessageEvent{}, source)
	if err != nil || got != "图中的表格：成功率 63%" {
		t.Fatalf("cached description=%q err=%v", got, err)
	}
	if store.saves != 0 {
		t.Fatal("cached description regenerated")
	}
}

func TestImageBudgetUsesBoundModelAndRequestCap(t *testing.T) {
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"intent": {ProfileID: "intent", Model: "judge"}}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.llmStore = &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "chat", Config: llm.ProviderConfig{Model: "chat", ContextWindowTokens: 200000}},
		{ID: "intent", Config: llm.ProviderConfig{Model: "judge", ContextWindowTokens: 64000, MaxOutputTokens: 2048}},
	}}}
	ctx := withLLMUsagePurpose(context.Background(), PurposeProactiveReplyRouter)
	window, reserve := runtime.imageRequestBudget(ctx, llm.GroupIntent, llm.GenerateRequest{})
	if window != 64000 || reserve != 2048 {
		t.Fatalf("window=%d reserve=%d", window, reserve)
	}
	window, reserve = runtime.imageRequestBudget(withContextBudgetCap(ctx, 32000), llm.GroupIntent, llm.GenerateRequest{MaxContextTokens: 16000, MaxOutputTokens: 512})
	if window != 16000 || reserve != 512 {
		t.Fatalf("capped window=%d reserve=%d", window, reserve)
	}
}

func TestImageBudgetRuntimeWrapperReplacesOverflow(t *testing.T) {
	source := imageSourceTestDataURL
	data, _, _ := decodeInlineHistoryImage(source)
	store := newRecallImageTestStore()
	store.descriptions[imageBytesSHA256(data)] = ImageDescriptionRecord{Description: "画面中的表格与文字"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	provider := &privacyRequestProvider{reply: "ok"}
	req := imageBudgetRequest(18)
	for i := 1; i < len(req.Messages[0].Parts); i++ {
		req.Messages[0].Parts[i].ImageURL = source
	}
	client := runtime.wrapLLMProviderForContext(context.Background(), provider)
	if _, err := client.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	images := 0
	for _, m := range provider.request.Messages {
		for _, p := range m.Parts {
			if p.Type == llm.ContentPartImageURL {
				images++
			}
		}
	}
	if images <= 0 || images >= 18 {
		t.Fatalf("overflow not converted in runtime chain: images=%d", images)
	}
}
