// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package localembed

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"unicode"
)

// tokenizer 复刻 HuggingFace tokenizers 的 BertNormalizer + BertPreTokenizer +
// WordPiece，参数取 bge-small-zh-v1.5 的 tokenizer.json：clean_text、
// handle_chinese_chars 开，lowercase 关、strip_accents 不设（跟着 lowercase 关）。
// 不小写是模型自带的配置，改了向量就和官方实现对不上。
type tokenizer struct {
	vocab   map[string]int32
	unk     int32
	cls     int32
	sep     int32
	maxRune int
}

func newTokenizer(vocabText []byte) (*tokenizer, error) {
	vocab := make(map[string]int32, 21128)
	scanner := bufio.NewScanner(bytes.NewReader(vocabText))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	var index int32
	for scanner.Scan() {
		token := strings.TrimRight(scanner.Text(), "\r")
		if _, exists := vocab[token]; !exists {
			vocab[token] = index
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	t := &tokenizer{vocab: vocab, maxRune: 100}
	for name, target := range map[string]*int32{"[UNK]": &t.unk, "[CLS]": &t.cls, "[SEP]": &t.sep} {
		id, ok := vocab[name]
		if !ok {
			return nil, fmt.Errorf("localembed: vocab missing %s", name)
		}
		*target = id
	}
	return t, nil
}

// encode 返回 [CLS] … [SEP]，总长不超过 maxLen（含两个特殊符号），超出的尾部截掉。
func (t *tokenizer) encode(text string, maxLen int) []int32 {
	ids := []int32{t.cls}
	limit := maxLen - 1
	for _, word := range preTokenize(normalize(text)) {
		for _, id := range t.wordPiece(word) {
			if len(ids) >= limit {
				return append(ids, t.sep)
			}
			ids = append(ids, id)
		}
	}
	return append(ids, t.sep)
}

func (t *tokenizer) wordPiece(word string) []int32 {
	runes := []rune(word)
	if len(runes) > t.maxRune {
		return []int32{t.unk}
	}
	var out []int32
	start := 0
	for start < len(runes) {
		end := len(runes)
		found := int32(-1)
		for end > start {
			piece := string(runes[start:end])
			if start > 0 {
				piece = "##" + piece
			}
			if id, ok := t.vocab[piece]; ok {
				found = id
				break
			}
			end--
		}
		if found < 0 {
			return []int32{t.unk}
		}
		out = append(out, found)
		start = end
	}
	return out
}

func normalize(text string) string {
	var builder strings.Builder
	builder.Grow(len(text) + 16)
	for _, r := range text {
		switch {
		case r == 0 || r == 0xfffd || isControl(r):
			continue
		case isWhitespace(r):
			builder.WriteByte(' ')
		case isChineseChar(r):
			builder.WriteByte(' ')
			builder.WriteRune(r)
			builder.WriteByte(' ')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// preTokenize 按空白切开，再把每个标点单独成词。
func preTokenize(text string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = current[:0]
		}
	}
	for _, r := range text {
		switch {
		case isWhitespace(r):
			flush()
		case isPunctuation(r):
			flush()
			words = append(words, string(r))
		default:
			current = append(current, r)
		}
	}
	flush()
	return words
}

func isWhitespace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' || unicode.Is(unicode.White_Space, r)
}

// isControl 对应 Unicode 的 C 大类（含未分配码位）：不是字母、标记、数字、标点、
// 符号、分隔符的都算。
func isControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	if r >= 0x20 && r < 0x7f {
		return false
	}
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z)
}

func isPunctuation(r rune) bool {
	if r < 128 {
		return (r >= 33 && r <= 47) || (r >= 58 && r <= 64) || (r >= 91 && r <= 96) || (r >= 123 && r <= 126)
	}
	return unicode.IsPunct(r)
}

func isChineseChar(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}
