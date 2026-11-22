// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package localembed 在本机跑一个小 BERT 句向量模型（bge-small-zh-v1.5），
// 让语义检索不用另配 embedding API。
//
// 纯 Go、不引 cgo：Diana 是一个二进制交叉编译五个平台，onnxruntime、llama.cpp
// 这类都要 cgo 或者按平台带动态库。bge-small-zh 只有 4 层、512 维，一条聊天
// 消息几十个 token，前向一次是几亿次乘加，纯 Go 标量循环也在几十毫秒量级，
// 向量化本来就在后台批量做，够用。
//
// 输出与 sentence-transformers 一致：取最后一层 [CLS] 的隐藏状态，L2 归一化。
package localembed

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
)

// maxTokens 是每条文本最多编码的 token 数。模型支持 512，但注意力随长度平方涨，
// 512 一条要一两秒，一批 16 条长消息会撑过后台索引的超时；聊天消息过 256 的
// 很少，开头那段也足够代表意思。
const maxTokens = 256

type bertConfig struct {
	HiddenSize       int     `json:"hidden_size"`
	IntermediateSize int     `json:"intermediate_size"`
	NumHeads         int     `json:"num_attention_heads"`
	NumLayers        int     `json:"num_hidden_layers"`
	MaxPositions     int     `json:"max_position_embeddings"`
	VocabSize        int     `json:"vocab_size"`
	LayerNormEps     float64 `json:"layer_norm_eps"`
}

type linear struct {
	weight []float32 // [out][in] 行优先，和 PyTorch nn.Linear 一致
	bias   []float32
	in     int
	out    int
}

type layerNorm struct {
	weight []float32
	bias   []float32
}

type bertLayer struct {
	query, key, value, attnOut linear
	attnNorm                   layerNorm
	intermediate, output       linear
	outNorm                    layerNorm
}

// Model 是加载好的模型，可并发调用。
type Model struct {
	cfg       bertConfig
	tok       *tokenizer
	wordEmb   []float32
	posEmb    []float32
	typeEmb   []float32
	embNorm   layerNorm
	layers    []bertLayer
	workers   int
	maxTokens int
}

// load 读取 HuggingFace 格式的 config.json、vocab.txt 和 model.safetensors。
func load(configJSON, vocabText []byte, weightsPath string) (*Model, error) {
	var cfg bertConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, fmt.Errorf("localembed: config: %w", err)
	}
	if cfg.HiddenSize <= 0 || cfg.NumHeads <= 0 || cfg.HiddenSize%cfg.NumHeads != 0 || cfg.NumLayers <= 0 {
		return nil, errors.New("localembed: invalid config")
	}
	tok, err := newTokenizer(vocabText)
	if err != nil {
		return nil, err
	}
	tensors, err := readSafetensors(weightsPath)
	if err != nil {
		return nil, err
	}
	h, inter := cfg.HiddenSize, cfg.IntermediateSize
	get := func(name string, size int) ([]float32, error) {
		value, ok := tensors[name]
		if !ok {
			return nil, fmt.Errorf("localembed: missing tensor %s", name)
		}
		if len(value) != size {
			return nil, fmt.Errorf("localembed: tensor %s has %d values, want %d", name, len(value), size)
		}
		return value, nil
	}
	var firstErr error
	must := func(name string, size int) []float32 {
		value, err := get(name, size)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return value
	}
	lin := func(prefix string, in, out int) linear {
		return linear{weight: must(prefix+".weight", in*out), bias: must(prefix+".bias", out), in: in, out: out}
	}
	norm := func(prefix string) layerNorm {
		return layerNorm{weight: must(prefix+".weight", h), bias: must(prefix+".bias", h)}
	}
	m := &Model{
		cfg:     cfg,
		tok:     tok,
		wordEmb: must("embeddings.word_embeddings.weight", cfg.VocabSize*h),
		posEmb:  must("embeddings.position_embeddings.weight", cfg.MaxPositions*h),
		typeEmb: must("embeddings.token_type_embeddings.weight", 2*h),
		embNorm: norm("embeddings.LayerNorm"),
		// 后台向量化不该占满整机：留一半核给消息处理。
		workers:   max(1, runtime.GOMAXPROCS(0)/2),
		maxTokens: min(cfg.MaxPositions, maxTokens),
	}
	for index := range cfg.NumLayers {
		prefix := fmt.Sprintf("encoder.layer.%d.", index)
		m.layers = append(m.layers, bertLayer{
			query:        lin(prefix+"attention.self.query", h, h),
			key:          lin(prefix+"attention.self.key", h, h),
			value:        lin(prefix+"attention.self.value", h, h),
			attnOut:      lin(prefix+"attention.output.dense", h, h),
			attnNorm:     norm(prefix + "attention.output.LayerNorm"),
			intermediate: lin(prefix+"intermediate.dense", h, inter),
			output:       lin(prefix+"output.dense", inter, h),
			outNorm:      norm(prefix + "output.LayerNorm"),
		})
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return m, nil
}

// Dimensions 是输出向量维度。
func (m *Model) Dimensions() int { return m.cfg.HiddenSize }

// Embed 逐条算句向量（已归一化）。ctx 取消时在两条之间停下。
func (m *Model) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for index, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[index] = m.embedOne(text)
	}
	return out, nil
}

func (m *Model) embedOne(text string) []float32 {
	ids := m.tok.encode(text, m.maxTokens)
	h := m.cfg.HiddenSize
	n := len(ids)
	hidden := make([]float32, n*h)
	for pos, id := range ids {
		row := hidden[pos*h : (pos+1)*h]
		word := m.wordEmb[int(id)*h : (int(id)+1)*h]
		position := m.posEmb[pos*h : (pos+1)*h]
		for i := range row {
			row[i] = word[i] + position[i] + m.typeEmb[i]
		}
	}
	m.layerNormRows(hidden, n, m.embNorm)

	inter := m.cfg.IntermediateSize
	q := make([]float32, n*h)
	k := make([]float32, n*h)
	v := make([]float32, n*h)
	ctxBuf := make([]float32, n*h)
	tmp := make([]float32, n*h)
	ffn := make([]float32, n*inter)
	for _, layer := range m.layers {
		m.apply(layer.query, hidden, q, n)
		m.apply(layer.key, hidden, k, n)
		m.apply(layer.value, hidden, v, n)
		m.attention(q, k, v, ctxBuf, n)
		m.apply(layer.attnOut, ctxBuf, tmp, n)
		addInto(tmp, hidden)
		m.layerNormRows(tmp, n, layer.attnNorm)
		copy(hidden, tmp)

		m.apply(layer.intermediate, hidden, ffn, n)
		for i, x := range ffn {
			ffn[i] = gelu(x)
		}
		m.apply(layer.output, ffn, tmp, n)
		addInto(tmp, hidden)
		m.layerNormRows(tmp, n, layer.outNorm)
		copy(hidden, tmp)
	}
	cls := make([]float32, h)
	copy(cls, hidden[:h])
	normalizeL2(cls)
	return cls
}

func (m *Model) attention(q, k, v, out []float32, n int) {
	h := m.cfg.HiddenSize
	heads := m.cfg.NumHeads
	dim := h / heads
	scale := float32(1 / math.Sqrt(float64(dim)))
	parallelFor(heads, min(heads, m.workers), func(from, to int) {
		scores := make([]float32, n)
		for head := from; head < to; head++ {
			offset := head * dim
			for i := range n {
				qi := q[i*h+offset : i*h+offset+dim]
				maxScore := float32(math.Inf(-1))
				for j := range n {
					s := dot(qi, k[j*h+offset:j*h+offset+dim]) * scale
					scores[j] = s
					maxScore = max(maxScore, s)
				}
				var sum float32
				for j := range n {
					e := float32(math.Exp(float64(scores[j] - maxScore)))
					scores[j] = e
					sum += e
				}
				dst := out[i*h+offset : i*h+offset+dim]
				clear(dst)
				for j := range n {
					weight := scores[j] / sum
					vj := v[j*h+offset : j*h+offset+dim]
					for d := range dst {
						dst[d] += weight * vj[d]
					}
				}
			}
		}
	})
}

// apply 算 dst = x·Wᵀ + b，按输出维度分给多个 goroutine。每次拿一行权重和
// 4 个 token 同时做点积，权重从内存读一遍用四次，这是纯 Go 下最主要的提速。
func (m *Model) apply(l linear, x, dst []float32, n int) {
	parallelFor(l.out, m.workers, func(from, to int) {
		for o := from; o < to; o++ {
			w := l.weight[o*l.in : (o+1)*l.in]
			bias := l.bias[o]
			t := 0
			for ; t+4 <= n; t += 4 {
				s0, s1, s2, s3 := dot4(w, x[t*l.in:(t+1)*l.in], x[(t+1)*l.in:(t+2)*l.in], x[(t+2)*l.in:(t+3)*l.in], x[(t+3)*l.in:(t+4)*l.in])
				dst[t*l.out+o] = s0 + bias
				dst[(t+1)*l.out+o] = s1 + bias
				dst[(t+2)*l.out+o] = s2 + bias
				dst[(t+3)*l.out+o] = s3 + bias
			}
			for ; t < n; t++ {
				dst[t*l.out+o] = dot(x[t*l.in:(t+1)*l.in], w) + bias
			}
		}
	})
}

func dot4(w, a, b, c, d []float32) (float32, float32, float32, float32) {
	n := len(w)
	a, b, c, d = a[:n], b[:n], c[:n], d[:n]
	var sa0, sa1, sb0, sb1, sc0, sc1, sd0, sd1 float32
	i := 0
	for ; i+2 <= n; i += 2 {
		w0, w1 := w[i], w[i+1]
		sa0 += a[i] * w0
		sa1 += a[i+1] * w1
		sb0 += b[i] * w0
		sb1 += b[i+1] * w1
		sc0 += c[i] * w0
		sc1 += c[i+1] * w1
		sd0 += d[i] * w0
		sd1 += d[i+1] * w1
	}
	for ; i < n; i++ {
		sa0 += a[i] * w[i]
		sb0 += b[i] * w[i]
		sc0 += c[i] * w[i]
		sd0 += d[i] * w[i]
	}
	return sa0 + sa1, sb0 + sb1, sc0 + sc1, sd0 + sd1
}

func (m *Model) layerNormRows(x []float32, n int, norm layerNorm) {
	h := m.cfg.HiddenSize
	eps := m.cfg.LayerNormEps
	if eps <= 0 {
		eps = 1e-12
	}
	for t := range n {
		row := x[t*h : (t+1)*h]
		var mean float64
		for _, value := range row {
			mean += float64(value)
		}
		mean /= float64(h)
		var variance float64
		for _, value := range row {
			d := float64(value) - mean
			variance += d * d
		}
		variance /= float64(h)
		inv := 1 / math.Sqrt(variance+eps)
		for i, value := range row {
			row[i] = float32((float64(value)-mean)*inv)*norm.weight[i] + norm.bias[i]
		}
	}
}

func parallelFor(total, workers int, fn func(from, to int)) {
	if workers <= 1 || total < 2*workers {
		fn(0, total)
		return
	}
	chunk := (total + workers - 1) / workers
	panics := make([]any, (total+chunk-1)/chunk)
	var wg sync.WaitGroup
	for index, from := 0, 0; from < total; index, from = index+1, from+chunk {
		to := min(total, from+chunk)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverInto(&panics[index])
			fn(from, to)
		}()
	}
	wg.Wait()
	// 工作 goroutine 里的 panic 挪回调用方再抛：不能让它带崩进程，也不能吞掉了
	// 返回一个算了一半的向量。
	for _, recovered := range panics {
		if recovered != nil {
			panic(recovered)
		}
	}
}

// recoverInto 必须被直接 defer：recover() 只在被 defer 的函数里直接调用才生效。
func recoverInto(dst *any) {
	if recovered := recover(); recovered != nil {
		*dst = recovered
	}
}

func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		s0 += a[i]*b[i] + a[i+4]*b[i+4]
		s1 += a[i+1]*b[i+1] + a[i+5]*b[i+5]
		s2 += a[i+2]*b[i+2] + a[i+6]*b[i+6]
		s3 += a[i+3]*b[i+3] + a[i+7]*b[i+7]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1) + (s2 + s3)
}

func addInto(dst, src []float32) {
	src = src[:len(dst)]
	for i := range dst {
		dst[i] += src[i]
	}
}

// gelu 是 PyTorch 默认的精确 erf 版本（HF 配置里的 "gelu"）。
func gelu(x float32) float32 {
	return float32(0.5 * float64(x) * (1 + math.Erf(float64(x)/math.Sqrt2)))
}

func normalizeL2(vector []float32) {
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range vector {
		vector[i] *= inv
	}
}

// readSafetensors 读出全部 F32 张量。格式：8 字节小端头长度 + JSON 头 + 原始数据。
func readSafetensors(path string) (map[string][]float32, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var headerLen uint64
	if err := binary.Read(file, binary.LittleEndian, &headerLen); err != nil {
		return nil, fmt.Errorf("localembed: safetensors header: %w", err)
	}
	if headerLen == 0 || headerLen > 16<<20 {
		return nil, errors.New("localembed: safetensors header too large")
	}
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(file, headerBytes); err != nil {
		return nil, err
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("localembed: safetensors header: %w", err)
	}
	type entry struct {
		Dtype   string   `json:"dtype"`
		Offsets [2]int64 `json:"data_offsets"`
	}
	base := int64(8 + headerLen)
	tensors := make(map[string][]float32, len(header))
	for name, raw := range header {
		if name == "__metadata__" {
			continue
		}
		var info entry
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, fmt.Errorf("localembed: tensor %s: %w", name, err)
		}
		if info.Dtype != "F32" {
			continue // position_ids 之类的整数张量用不到
		}
		size := info.Offsets[1] - info.Offsets[0]
		if size < 0 || size%4 != 0 {
			return nil, fmt.Errorf("localembed: tensor %s has bad offsets", name)
		}
		buffer := make([]byte, size)
		if _, err := file.ReadAt(buffer, base+info.Offsets[0]); err != nil {
			return nil, fmt.Errorf("localembed: tensor %s: %w", name, err)
		}
		values := make([]float32, size/4)
		for i := range values {
			values[i] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[i*4:]))
		}
		tensors[name] = values
	}
	return tensors, nil
}
