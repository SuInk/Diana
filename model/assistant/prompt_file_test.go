// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 导出的文件列出每一段，改过的写改过的；读回来只剩改过的那几段。
func TestPromptFileRoundTrip(t *testing.T) {
	spec := promptPersonaClosingAnchorSpec
	overrides := PromptOverrides{spec.Key: "最后：照人设说话。"}
	raw, err := RenderPromptFile(overrides, "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"format_version: 1", "diana_version: v9.9.9", "最后：照人设说话。", promptReplyDepthAnchorSpec.Key + ": |"} {
		if !strings.Contains(text, want) {
			t.Fatalf("exported file is missing %q", want)
		}
	}
	for _, spec := range PromptSpecs() {
		if !strings.Contains(text, "  "+spec.Key+":") {
			t.Fatalf("exported file is missing prompt %s", spec.Key)
		}
	}
	parsed, err := ParsePromptFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Changed != 1 || parsed.Overrides[spec.Key] != "最后：照人设说话。" || parsed.DianaVersion != "v9.9.9" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

// 宽松读：没写的段落用默认值，登记表里没有的键跳过并报出来。
func TestPromptFileIsLenientAboutMissingAndUnknownKeys(t *testing.T) {
	raw := "format_version: 1\nprompts:\n  reply.style.closing_anchor: 改过了\n  reply.style.voice.enders: 旧版本删掉的\n"
	parsed, err := ParsePromptFile([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Changed != 1 || len(parsed.Unknown) != 1 || parsed.Unknown[0] != "reply.style.voice.enders" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestPromptFileRejectsBadInput(t *testing.T) {
	for name, raw := range map[string]string{
		"空文件":  "  ",
		"没有版本": "prompts:\n  reply.style.closing_anchor: x\n",
		"版本太新": "format_version: 99\nprompts: {}\n",
		"缩进错了": "format_version: 1\nprompts:\n  a: |\n x\n  b: y\n",
	} {
		if _, err := ParsePromptFile([]byte(raw)); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}
