package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 没有覆盖时，发送前审核这几段必须和拆分前逐字节相同：审核提示词每轮都发，
// 多一个换行也会让前缀缓存和既有测试一起失效。
func TestAuditPromptDefaultsAreUnchanged(t *testing.T) {
	var cfg BotConfig
	for _, tc := range []struct {
		name, got, want string
	}{
		{"quality", cfg.prompt(promptReplyQualitySpec), proactiveReplyQualityPrompt},
		{"compression", cfg.prompt(promptReplyCompressionSpec), replyCompressionPrompt},
		{"semantic", cfg.prompt(promptReplySemanticDedupSpec), semanticReplyPrompt},
		{"pause", cfg.prompt(promptReplyPauseHintSpec), replyPauseHintPrompt},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s 默认提示词变了", tc.name)
		}
	}
	if !strings.HasPrefix(proactiveReplyQualityContract, "\n\n只输出一个合法 JSON 对象") {
		t.Fatal("审核的输出格式应当从 JSON 说明开始锁定")
	}
	if !strings.HasPrefix(replyQualityPromptForConfig(cfg), proactiveReplyQualityPrompt+"\n正常的寒暄") {
		t.Fatal("默认配置下审核提示词的拼接顺序变了")
	}
}

// 改了审核正文，输出格式照样接在后面；管理员的账号安全规则仍排在格式之后。
func TestAuditQualityOverrideKeepsContractAndPolicyOrder(t *testing.T) {
	cfg := BotConfig{
		PromptOverrides:               PromptOverrides{promptReplyQualitySpec.Key: "自定义审核正文"},
		ReplyAccountSafetyAuditPrompt: "自定义安全规则",
	}
	prompt := replyQualityPromptForConfig(cfg)
	if !strings.HasPrefix(prompt, "自定义审核正文"+proactiveReplyQualityContract) {
		t.Fatalf("覆盖后的正文或锁定的输出格式不对：%q", prompt[:min(len(prompt), 200)])
	}
	if strings.Contains(prompt, "你是机器人回复的发送前审核器") {
		t.Fatal("默认正文不该留在覆盖后的提示词里")
	}
	contractAt := strings.Index(prompt, "只输出一个合法 JSON 对象")
	policyAt := strings.Index(prompt, "自定义安全规则")
	if contractAt < 0 || policyAt < contractAt {
		t.Fatalf("账号安全规则应当接在输出格式之后：contract=%d policy=%d", contractAt, policyAt)
	}
}

type auditOverrideProvider struct {
	mu      sync.Mutex
	systems []string
	text    string
}

func (p *auditOverrideProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			p.systems = append(p.systems, m.Content)
		}
	}
	return &llm.GenerateResponse{Text: p.text}, nil
}

func (p *auditOverrideProvider) sawSystem(want string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, system := range p.systems {
		if strings.Contains(system, want) {
			return true
		}
	}
	return false
}

func TestSemanticDedupOverrideReachesModelWithContract(t *testing.T) {
	p := &auditOverrideProvider{text: `{"action":"keep","confidence":0.99}`}
	r := topicTestRuntime(p)
	event := directedGroupMessage("m", "u", "新的问题")
	g, release, err := r.lockSemanticReply(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	g.remember("原问题", "原有说明")
	cfg := BotConfig{PromptOverrides: PromptOverrides{promptReplySemanticDedupSpec.Key: "自定义去重正文"}}
	if _, err := r.deduplicateReply(context.Background(), event, "新的问题", "候选", cfg, g, true); err != nil {
		t.Fatal(err)
	}
	if !p.sawSystem("自定义去重正文" + semanticReplyContract) {
		t.Fatalf("去重调用没有用上覆盖正文加锁定格式：%q", p.systems)
	}
}

func TestNoticeAndPauseHintOverridesReachModel(t *testing.T) {
	provider := &rejectionRewriteProvider{text: "这次没接住，晚点再来。"}
	runtime := NewRuntime(BotConfig{PromptOverrides: PromptOverrides{promptRejectionNoticeSpec.Key: "自定义拒绝改写"}}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	if _, ok := runtime.rewriteRejectionNotice(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "u"}, llm.ErrUnverifiedRejection); !ok {
		t.Fatal("改写应当成功")
	}
	var system strings.Builder
	for _, m := range provider.rewrite.Messages {
		if m.Role == llm.RoleSystem {
			system.WriteString(m.Content)
		}
	}
	if !strings.Contains(system.String(), "自定义拒绝改写") || strings.Contains(system.String(), "把收到的上游拒绝文案改写成") {
		t.Fatalf("拒绝改写没有用上覆盖正文：%q", system.String())
	}

	hintProvider := &auditOverrideProvider{text: "我去忙一会儿"}
	hintRuntime := NewRuntime(BotConfig{PromptOverrides: PromptOverrides{promptReplyPauseHintSpec.Key: "自定义收声提示"}}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return hintProvider, nil })
	if _, err := hintRuntime.generateReplyPauseHint(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	if !hintProvider.sawSystem("自定义收声提示") {
		t.Fatalf("收声提示没有用上覆盖正文：%q", hintProvider.systems)
	}
}
