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
