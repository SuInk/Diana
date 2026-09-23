// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 登记表是界面的唯一来源：每条都得有标题、说明和默认正文，分组必须是界面认识的，
// 声明的占位符必须真的出现在默认正文里，否则界面上的提示就是在说谎。
func TestPromptSpecsAreComplete(t *testing.T) {
	groups := map[PromptGroup]bool{}
	for _, group := range PromptGroups() {
		groups[group.ID] = true
	}
	specs := PromptSpecs()
	if len(specs) == 0 {
		t.Fatal("no prompt specs registered")
	}
	for _, spec := range specs {
		if strings.TrimSpace(spec.Title) == "" || strings.TrimSpace(spec.Usage) == "" {
			t.Errorf("%s: missing title or usage", spec.Key)
		}
		if strings.TrimSpace(spec.Default) == "" {
			t.Errorf("%s: empty default", spec.Key)
		}
		if !groups[spec.Group] {
			t.Errorf("%s: unknown group %q", spec.Key, spec.Group)
		}
		for _, variable := range spec.Vars {
			if !strings.Contains(spec.Default+spec.Contract, "{"+variable.Name+"}") {
				t.Errorf("%s: declared placeholder {%s} missing from default", spec.Key, variable.Name)
			}
		}
		if len([]rune(spec.Default)) > PromptOverrideMaxRunes {
			t.Errorf("%s: default longer than the override limit", spec.Key)
		}
	}
}

func TestPromptOverrideReplacesBodyButKeepsContract(t *testing.T) {
	spec := &PromptSpec{Key: "test.contract", Default: "默认正文", Contract: "\n只输出 JSON。"}
	if got := PromptOverrides(nil).text(spec); got != "默认正文\n只输出 JSON。" {
		t.Fatalf("default text = %q", got)
	}
	if got := (PromptOverrides{"test.contract": "  改过的正文 \n"}).text(spec); got != "改过的正文\n只输出 JSON。" {
		t.Fatalf("override text = %q", got)
	}
}

func TestPromptRenderOnlyReplacesDeclaredPlaceholders(t *testing.T) {
	spec := &PromptSpec{Key: "test.render", Default: `上限 {limit} 字，示例 {"a":1} 和 {unknown}`}
	got := PromptOverrides(nil).render(spec, map[string]string{"limit": "300"})
	if got != `上限 300 字，示例 {"a":1} 和 {unknown}` {
		t.Fatalf("render = %q", got)
	}
}

// 改回默认值再保存不能留下一条覆盖：那会把当前默认文案冻结在这台机器人上。
func TestNormalizePromptOverridesDropsDefaultsAndUnknownKeys(t *testing.T) {
	got := normalizePromptOverrides(PromptOverrides{
		promptWakeOnlySpec.Key:     "\n" + defaultPromptWakeOnly + "\n",
		promptImageOnlySpec.Key:    "看图说话\r\n就行",
		"removed.in.new.version":   "旧键",
		promptTimeTemplateSpec.Key: "   ",
	})
	if len(got) != 1 || got[promptImageOnlySpec.Key] != "看图说话\n就行" {
		t.Fatalf("normalized = %#v", got)
	}
	if normalizePromptOverrides(PromptOverrides{promptWakeOnlySpec.Key: defaultPromptWakeOnly}) != nil {
		t.Fatal("overrides equal to defaults should normalize to nil")
	}
}

func TestPromptOverridesValidateLength(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.PromptOverrides = PromptOverrides{promptWakeOnlySpec.Key: strings.Repeat("长", PromptOverrideMaxRunes+1)}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), promptWakeOnlySpec.Title) {
		t.Fatalf("Validate() = %v, want length error naming the prompt", err)
	}
}

// 旧字段里存的多半是某一版默认值：当前默认值和历史默认值都不算用户写的，
// 迁移时丢掉；只有真的改过的才搬进覆盖表。
func TestLegacyPromptFieldsMigrateOnlyUserText(t *testing.T) {
	// git 历史里最早那版纯文本规则，存量库里就可能躺着它。
	const fossilPlaintext = "QQ 消息不渲染 Markdown，回复必须用纯文本：不要输出 **、#、```、表格或链接语法，列表直接写 1. 2. 3.；回复较长时用 <botbr> 分成两三句一段。"
	cfg := BotConfig{
		PromptChineseSlangText:     defaultPromptChineseSlang,
		PromptPlaintextRulesText:   fossilPlaintext,
		PromptWakeOnlyText:         "叫我就接着刚才的话说。",
		ProactiveReplyPrompt:       legacySingleMessageProactiveReplyPrompt,
		ProactiveReplyRouterPrompt: defaultProactiveReplyRouterPrompt,
	}.WithDefaults()
	if cfg.PromptChineseSlangText != "" || cfg.PromptPlaintextRulesText != "" || cfg.PromptWakeOnlyText != "" || cfg.ProactiveReplyPrompt != "" || cfg.ProactiveReplyRouterPrompt != "" {
		t.Fatalf("legacy fields should be cleared after migration: %#v", cfg)
	}
	if len(cfg.PromptOverrides) != 1 || cfg.PromptOverrides[promptWakeOnlySpec.Key] != "叫我就接着刚才的话说。" {
		t.Fatalf("overrides = %#v, want only the user-written wake text", cfg.PromptOverrides)
	}
	if got := cfg.prompt(promptPlaintextRulesSpec); got != defaultPromptPlaintextRules {
		t.Fatalf("fossil plaintext rules should fall back to the current default, got %q", got)
	}
	if got := cfg.prompt(promptProactiveReplySpec); got != defaultProactiveReplyPrompt {
		t.Fatalf("legacy proactive prompt should fall back to the current default, got %q", got)
	}
}

func TestLegacyPromptMigrationPrefersNewOverride(t *testing.T) {
	cfg := BotConfig{
		PromptWakeOnlyText: "旧字段里的写法",
		PromptOverrides:    PromptOverrides{promptWakeOnlySpec.Key: "新界面里的写法"},
	}.WithDefaults()
	if got := cfg.prompt(promptWakeOnlySpec); got != "新界面里的写法" {
		t.Fatalf("prompt = %q", got)
	}
}

// cfg 按值传递但 map 不是：迁移不能改到调用方手里那份覆盖表。
func TestLegacyPromptMigrationDoesNotMutateSharedOverrides(t *testing.T) {
	shared := PromptOverrides{promptImageOnlySpec.Key: "看图"}
	_ = BotConfig{PromptWakeOnlyText: "自定义", PromptOverrides: shared}.WithDefaults()
	if len(shared) != 1 {
		t.Fatalf("shared overrides were mutated: %#v", shared)
	}
}

func TestPromptOverridesRoundTripThroughPayload(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.PromptOverrides = PromptOverrides{promptImageOnlySpec.Key: "看图"}
	data, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	restored := ConfigFromPayload(payload, DefaultBotConfig()).WithDefaults()
	if got := restored.prompt(promptImageOnlySpec); got != "看图" {
		t.Fatalf("round trip lost the override: %q", got)
	}
}
