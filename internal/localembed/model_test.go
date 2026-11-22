// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package localembed

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
)

// testdata/tiny 是用 transformers 随机初始化的 2 层小 BERT（LayerNorm 也随机化了），
// expected.json 是 PyTorch 算出的 token 和归一化 [CLS] 向量。对上了说明前向的
// 每一步（嵌入、注意力、GELU、残差、LayerNorm、截断）都和官方实现一致。
func TestModelMatchesPyTorchReference(t *testing.T) {
	config, err := os.ReadFile("testdata/tiny/config.json")
	if err != nil {
		t.Fatal(err)
	}
	vocab, err := os.ReadFile("testdata/tiny/vocab.txt")
	if err != nil {
		t.Fatal(err)
	}
	model, err := load(config, vocab, "testdata/tiny/model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/tiny/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text   string    `json:"text"`
		IDs    []int32   `json:"ids"`
		Vector []float32 `json:"vector"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	texts := make([]string, len(cases))
	for index, c := range cases {
		texts[index] = c.Text
	}
	// 小模型的 max_position_embeddings 是 12，最后一条会被截断。
	vectors, err := model.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	for index, c := range cases {
		if got := model.tok.encode(c.Text, model.maxTokens); !slices.Equal(got, c.IDs) {
			t.Errorf("%q ids = %v, want %v", c.Text, got, c.IDs)
		}
		if len(vectors[index]) != len(c.Vector) {
			t.Fatalf("%q dims = %d, want %d", c.Text, len(vectors[index]), len(c.Vector))
		}
		for d := range c.Vector {
			if math.Abs(float64(vectors[index][d]-c.Vector[d])) > 1e-4 {
				t.Fatalf("%q dim %d = %v, want %v", c.Text, d, vectors[index][d], c.Vector[d])
			}
		}
	}
}

func TestModelMultiWorkerMatchesSingleWorker(t *testing.T) {
	config, _ := os.ReadFile("testdata/tiny/config.json")
	vocab, _ := os.ReadFile("testdata/tiny/vocab.txt")
	model, err := load(config, vocab, "testdata/tiny/model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	text := "1 2 3 a b c 今天吃什么"
	model.workers = 1
	single := model.embedOne(text)
	model.workers = 4
	parallel := model.embedOne(text)
	for d := range single {
		if math.Abs(float64(single[d]-parallel[d])) > 1e-6 {
			t.Fatalf("dim %d: single %v, parallel %v", d, single[d], parallel[d])
		}
	}
}

func TestEmbedStopsWhenContextCancelled(t *testing.T) {
	config, _ := os.ReadFile("testdata/tiny/config.json")
	vocab, _ := os.ReadFile("testdata/tiny/vocab.txt")
	model, err := load(config, vocab, "testdata/tiny/model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := model.Embed(ctx, []string{"今天"}); err == nil {
		t.Fatal("cancelled context should stop embedding")
	}
}

func TestLoadRejectsMismatchedWeights(t *testing.T) {
	vocab, _ := os.ReadFile("testdata/tiny/vocab.txt")
	// bge 的真实配置配上小模型的权重：维度对不上必须报错，不能读越界或算出垃圾。
	if _, err := load(configJSON, vocab, "testdata/tiny/model.safetensors"); err == nil {
		t.Fatal("mismatched config and weights should fail")
	}
}

// 真实词表下的分词结果，期望值来自 transformers 的 BertTokenizerFast。
func TestTokenizerMatchesHuggingFaceOnRealVocab(t *testing.T) {
	tok, err := newTokenizer(vocabText)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/bge_tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string  `json:"text"`
		IDs  []int32 `json:"ids"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if got := tok.encode(c.Text, 512); !slices.Equal(got, c.IDs) {
			t.Errorf("%q\n got %v\nwant %v", c.Text, got, c.IDs)
		}
	}
}

// 工作 goroutine 里的 panic 要回到调用方，由调用方的兜底处理，不能带崩进程。
func TestParallelForRethrowsWorkerPanicInCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "boom" {
			t.Fatalf("recovered = %v, want boom", recovered)
		}
	}()
	parallelFor(8, 4, func(from, _ int) {
		if from >= 4 {
			panic("boom")
		}
	})
	t.Fatal("parallelFor should panic")
}
