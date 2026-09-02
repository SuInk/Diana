// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func v2CardJSON(t *testing.T) []byte {
	t.Helper()
	card := map[string]any{
		"spec":         "chara_card_v2",
		"spec_version": "2.0",
		"data": map[string]any{
			"name":        "然然",
			"description": "{{char}}是枝江的看板娘，喜欢和{{user}}聊天。",
			"personality": "爱撒娇但正事靠谱",
			"scenario":    "故事发生在虚构城市枝江。",
			"first_mes":   "哇，是{{user}}！今天想聊点什么？",
			"mes_example": "<START>\n{{user}}: 今天好累。\n{{char}}: 那就歇一会嘛，我陪你。",
			"character_book": map[string]any{
				"name": "枝江设定",
				"entries": []map[string]any{
					{"keys": []string{"港口"}, "name": "港口", "content": "枝江港常年有雾。", "insertion_order": 1},
				},
			},
		},
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseSillyTavernCharacterCardV2JSON(t *testing.T) {
	result, err := ParseSillyTavernCharacterCard(v2CardJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Persona.Name != "然然" {
		t.Fatalf("persona name = %q", result.Persona.Name)
	}
	prompt := result.Persona.SystemPrompt
	for _, want := range []string{"你是然然。", "看板娘", "性格与特质：爱撒娇但正事靠谱", "场景与背景：故事发生在虚构城市枝江。", "对话示例", "开场白参考"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %q", want, prompt)
		}
	}
	// 宏要展开：{{char}} 换成名字，{{user}} 换成「对方」，<START> 不残留。
	if strings.Contains(prompt, "{{char}}") || strings.Contains(prompt, "{{user}}") || strings.Contains(strings.ToUpper(prompt), "<START>") {
		t.Fatalf("macros survived: %q", prompt)
	}
	if !strings.Contains(prompt, "然然是枝江的看板娘") || !strings.Contains(prompt, "对方: 今天好累。") {
		t.Fatalf("macro expansion wrong: %q", prompt)
	}
	// 内嵌世界书跟着出来。
	if result.BookName != "枝江设定" || len(result.BookNodes) != 1 || result.BookNodes[0].Title != "港口" {
		t.Fatalf("book = %q %#v", result.BookName, result.BookNodes)
	}
}

func TestParseSillyTavernCharacterCardV1Flat(t *testing.T) {
	raw := []byte(`{"name": "老猫", "description": "话不多的技术群管。", "first_mes": "有事说事。"}`)
	result, err := ParseSillyTavernCharacterCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Persona.Name != "老猫" || !strings.Contains(result.Persona.SystemPrompt, "话不多的技术群管") {
		t.Fatalf("persona = %#v", result.Persona)
	}
	if len(result.BookNodes) != 0 {
		t.Fatalf("book nodes from bookless card: %#v", result.BookNodes)
	}
}

// buildCardPNG 拼一个最小可扫的 PNG：签名 + 一个假 IHDR + tEXt(chara) + IEND。
func buildCardPNG(t *testing.T, keyword string, payload []byte) []byte {
	t.Helper()
	chunk := func(chunkType string, data []byte) []byte {
		var buffer bytes.Buffer
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(data)))
		buffer.Write(length)
		buffer.WriteString(chunkType)
		buffer.Write(data)
		buffer.Write([]byte{0, 0, 0, 0}) // CRC 不校验
		return buffer.Bytes()
	}
	text := append(append([]byte(keyword), 0), []byte(base64.StdEncoding.EncodeToString(payload))...)
	var png bytes.Buffer
	png.Write(pngSignature)
	png.Write(chunk("IHDR", make([]byte, 13)))
	png.Write(chunk("tEXt", text))
	png.Write(chunk("IEND", nil))
	return png.Bytes()
}

func TestParseSillyTavernCharacterCardPNG(t *testing.T) {
	result, err := ParseSillyTavernCharacterCard(buildCardPNG(t, "chara", v2CardJSON(t)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Persona.Name != "然然" || len(result.BookNodes) != 1 {
		t.Fatalf("png card = %#v", result)
	}

	// 没内嵌卡的 PNG 要报得出原因，不能说成「不是 JSON」。
	var plain bytes.Buffer
	plain.Write(pngSignature)
	if _, err := ParseSillyTavernCharacterCard(plain.Bytes()); err != errCharacterCardPNGWithoutCard {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSillyTavernCharacterCardRejectsGarbage(t *testing.T) {
	if _, err := ParseSillyTavernCharacterCard([]byte("not json")); err == nil {
		t.Fatal("garbage was accepted")
	}
	// 没名字没简介的空壳卡拒收。
	if _, err := ParseSillyTavernCharacterCard([]byte(`{"first_mes": "你好"}`)); err != errCharacterCardEmpty {
		t.Fatalf("err = %v", err)
	}
}

func TestComposeCharacterCardPromptHonorsLengthBudget(t *testing.T) {
	long := strings.Repeat("很长的设定。", 400) // 2400 字
	prompt := composeCharacterCardPrompt(sillyTavernCardData{
		Name:        "长话痨",
		Description: long,
		Personality: long,
		Scenario:    "短场景。",
	})
	if len([]rune(prompt)) > personaPromptMaxRunes {
		t.Fatalf("prompt over budget: %d runes", len([]rune(prompt)))
	}
	// 装不下的段整段放弃：description 在前保住，personality 被丢弃后 scenario 也不该越过它插队……
	// 顺序是固定的，越靠后的段越先被舍弃。
	if !strings.Contains(prompt, "你是长话痨。") {
		t.Fatalf("intro missing: %q", prompt[:64])
	}
	if strings.Contains(prompt, "场景与背景") && !strings.Contains(prompt, "性格与特质") {
		t.Fatal("later section jumped over a dropped earlier section")
	}
}
