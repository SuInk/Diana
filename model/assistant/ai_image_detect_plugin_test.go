// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func aiImageTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func aiImageTestJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withPNGChunk 把一个块插到 IHDR 后面，和真实工具写入的位置一致。
func withPNGChunk(data []byte, chunkType string, payload []byte) []byte {
	chunk := make([]byte, 0, len(payload)+12)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(payload)))
	chunk = append(chunk, chunkType...)
	chunk = append(chunk, payload...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte(chunkType), payload...)))
	insertAt := 8 + 12 + 13 // 签名 + IHDR
	out := append([]byte(nil), data[:insertAt]...)
	out = append(out, chunk...)
	return append(out, data[insertAt:]...)
}

// withJPEGSegment 把一个 APPn 段插到 SOI 后面。
func withJPEGSegment(data []byte, marker byte, payload []byte) []byte {
	segment := []byte{0xFF, marker}
	segment = binary.BigEndian.AppendUint16(segment, uint16(len(payload)+2))
	segment = append(segment, payload...)
	out := append([]byte(nil), data[:2]...)
	out = append(out, segment...)
	return append(out, data[2:]...)
}

func zlibBytes(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func xmpSegment(body string) []byte {
	return append([]byte("http://ns.adobe.com/xap/1.0/\x00"), body...)
}

func findingEvidence(report AIImageReport) string {
	var parts []string
	for _, finding := range report.Findings {
		parts = append(parts, finding.Evidence)
	}
	return strings.Join(parts, " | ")
}

func TestDetectAIImageMarkersRecognizesKnownLabels(t *testing.T) {
	plainPNG := aiImageTestPNG(t)
	plainJPEG := aiImageTestJPEG(t)
	webp := func(fourCC string, payload []byte) []byte {
		var chunk []byte
		chunk = append(chunk, fourCC...)
		chunk = binary.LittleEndian.AppendUint32(chunk, uint32(len(payload)))
		chunk = append(chunk, payload...)
		if len(payload)%2 == 1 {
			chunk = append(chunk, 0)
		}
		out := []byte("RIFF")
		out = binary.LittleEndian.AppendUint32(out, uint32(4+len(chunk)))
		out = append(out, "WEBP"...)
		return append(out, chunk...)
	}

	cases := []struct {
		name     string
		image    []byte
		verdict  string
		evidence string
	}{
		{
			name:     "SD WebUI 写在 PNG tEXt 里的参数",
			image:    withPNGChunk(plainPNG, "tEXt", []byte("parameters\x001girl, masterpiece\nNegative prompt: lowres\nSteps: 20, Sampler: Euler a, CFG scale: 7")),
			verdict:  aiImageVerdictMarked,
			evidence: "Stable Diffusion WebUI",
		},
		{
			name:     "ComfyUI 压缩的工作流",
			image:    withPNGChunk(plainPNG, "zTXt", append([]byte("prompt\x00\x00"), zlibBytes(t, `{"3":{"class_type":"KSampler","inputs":{}}}`)...)),
			verdict:  aiImageVerdictMarked,
			evidence: "ComfyUI",
		},
		{
			name:     "PNG iTXt 压缩的 XMP 带 IPTC AI 来源类型",
			image:    withPNGChunk(plainPNG, "iTXt", append([]byte("XML:com.adobe.xmp\x00\x01\x00\x00\x00"), zlibBytes(t, `<Iptc4xmpExt:DigitalSourceType>http://cv.iptc.org/newscodes/digitalsourcetype/trainedAlgorithmicMedia</Iptc4xmpExt:DigitalSourceType>`)...)),
			verdict:  aiImageVerdictMarked,
			evidence: "AI 生成（trainedAlgorithmicMedia）",
		},
		{
			name:     "JPEG XMP 标 AI 参与编辑",
			image:    withJPEGSegment(plainJPEG, 0xE1, xmpSegment(`<rdf:li>http://cv.iptc.org/newscodes/digitalsourcetype/compositeWithTrainedAlgorithmicMedia</rdf:li>`)),
			verdict:  aiImageVerdictMarked,
			evidence: "AI 参与合成或编辑",
		},
		{
			name:     "Google 的 Made with Google AI 标注",
			image:    withJPEGSegment(plainJPEG, 0xE1, xmpSegment(`<photoshop:Credit>Made with Google AI</photoshop:Credit>`)),
			verdict:  aiImageVerdictMarked,
			evidence: "SynthID",
		},
		{
			name:     "国内 AIGC 隐式标识",
			image:    withJPEGSegment(plainJPEG, 0xE1, xmpSegment(`<xmp:AIGC>{"Label":"1","ContentProducer":"001191110108MA01KP2T5U00000"}</xmp:AIGC>`)),
			verdict:  aiImageVerdictMarked,
			evidence: "AIGC 隐式标识",
		},
		{
			name:     "EXIF Software 写着 Midjourney",
			image:    withJPEGSegment(plainJPEG, 0xE1, []byte("Exif\x00\x00MM\x00*Software\x00Midjourney v7")),
			verdict:  aiImageVerdictMarked,
			evidence: "Midjourney",
		},
		{
			name:     "WebP XMP 里的 SynthID 声明",
			image:    webp("XMP ", []byte(`<x:xmpmeta>SynthID watermark</x:xmpmeta>`)),
			verdict:  aiImageVerdictMarked,
			evidence: "SynthID",
		},
		{
			name:     "只有 C2PA 凭证没有 AI 来源类型只算线索",
			image:    withJPEGSegment(plainJPEG, 0xEB, []byte("JP\x00\x00jumbc2pa\x00c2pa.actions claim_generator=Photoshop")),
			verdict:  aiImageVerdictHint,
			evidence: "C2PA",
		},
		{
			name:     "OpenAI 字样只算线索",
			image:    withJPEGSegment(plainJPEG, 0xFE, []byte("shared from OpenAI forum")),
			verdict:  aiImageVerdictHint,
			evidence: "OpenAI",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := detectAIImageMarkers(tc.image)
			if report.Verdict != tc.verdict {
				t.Fatalf("verdict = %s，线索：%s", report.Verdict, findingEvidence(report))
			}
			if !strings.Contains(findingEvidence(report), tc.evidence) {
				t.Fatalf("没有找到 %q，线索：%s", tc.evidence, findingEvidence(report))
			}
		})
	}
}

// 普通照片不能被误判，也要告诉模型「完全没有元数据」这个事实。
func TestDetectAIImageMarkersPlainImages(t *testing.T) {
	for name, data := range map[string][]byte{"png": aiImageTestPNG(t), "jpeg": aiImageTestJPEG(t)} {
		report := detectAIImageMarkers(data)
		if report.Verdict != aiImageVerdictNone || len(report.Findings) != 0 {
			t.Fatalf("%s 误判：%#v", name, report)
		}
		if !report.NoMetadata {
			t.Fatalf("%s 应当标记为没有元数据", name)
		}
		if report.Format != name {
			t.Fatalf("format = %s, want %s", report.Format, name)
		}
	}
	// 普通相机 EXIF 不算 AI 线索。
	camera := withJPEGSegment(aiImageTestJPEG(t), 0xE1, []byte("Exif\x00\x00MM\x00*Canon EOS R5\x00Adobe Lightroom"))
	if report := detectAIImageMarkers(camera); report.Verdict != aiImageVerdictNone || report.NoMetadata {
		t.Fatalf("相机照片误判：%#v", report)
	}
}

// 聊天里什么图都可能来：截断的、乱写长度的、根本不是图的，都不能让它崩。
func TestDetectAIImageMarkersToleratesMalformedInput(t *testing.T) {
	withMeta := withPNGChunk(aiImageTestPNG(t), "tEXt", []byte("parameters\x00Steps: 20, Sampler: Euler, CFG scale: 7"))
	inputs := [][]byte{
		nil,
		[]byte("not an image"),
		withMeta[:20],
		withMeta[:len(withMeta)-30],
		{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 'E'},
		{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01},
		[]byte("RIFF\x00\x00\x00\x00WEBPXMP \xff\xff\xff\x7f"),
		withPNGChunk(aiImageTestPNG(t), "zTXt", []byte("prompt\x00\x00not zlib")),
		withPNGChunk(aiImageTestPNG(t), "iTXt", []byte("k\x00\x01")),
	}
	for index, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("输入 %d 触发 panic：%v", index, recovered)
				}
			}()
			_ = detectAIImageMarkers(input)
		}()
	}
	// 截掉尾巴但元数据块完整时，结论照样得出来。
	if report := detectAIImageMarkers(withMeta[:len(withMeta)-12]); report.Verdict != aiImageVerdictMarked {
		t.Fatalf("截断图片丢了结论：%#v", report)
	}
}

func TestAIImageDetectSynthIDService(t *testing.T) {
	var calls atomic.Int32
	var gotAuth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		if _, _, err := r.FormFile("file"); err != nil {
			t.Errorf("没有收到 file 字段：%v", err)
		}
		_, _ = w.Write([]byte(`{"detected":true,"confidence":97,"message":"SynthID watermark found"}`))
	}))
	defer server.Close()

	plugin := NewAIImageDetectPlugin(server.Client())
	cfg := aiImageDetectConfigFromSettings(SettingValues{
		aiImageDetectSettingSynthIDURL: server.URL,
		aiImageDetectSettingSynthIDKey: "secret",
	})
	report := plugin.detect(context.Background(), cfg, aiImageTestPNG(t))
	if calls.Load() != 1 || gotAuth.Load() != "Bearer secret" {
		t.Fatalf("calls = %d, auth = %v", calls.Load(), gotAuth.Load())
	}
	if report.Verdict != aiImageVerdictMarked || !report.SynthID.Checked || report.SynthID.Detected == nil || !*report.SynthID.Detected {
		t.Fatalf("report = %#v", report)
	}
	if report.SynthID.Confidence == nil || *report.SynthID.Confidence != 0.97 {
		t.Fatalf("百分比置信度没有归一化：%#v", report.SynthID.Confidence)
	}
}

// 服务返回说不清结论时，不能当成「没有水印」。
func TestAIImageDetectSynthIDServiceWithoutVerdict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()

	plugin := NewAIImageDetectPlugin(server.Client())
	cfg := aiImageDetectConfigFromSettings(SettingValues{aiImageDetectSettingSynthIDURL: server.URL})
	report := plugin.detect(context.Background(), cfg, aiImageTestPNG(t))
	if report.SynthID.Detected != nil || report.SynthID.Error == "" {
		t.Fatalf("report = %#v", report)
	}
	if summary := aiImageDetectSummary(report); !strings.Contains(summary, "没查成") {
		t.Fatalf("summary = %s", summary)
	}
}

// 配歪的地址不能拿来把图片发出去；超限的图不送检，但本地检测照做。
func TestAIImageDetectSynthIDServiceGuards(t *testing.T) {
	cfg := aiImageDetectConfigFromSettings(SettingValues{aiImageDetectSettingSynthIDURL: "http://example.com/detect"})
	if cfg.SynthIDURL != "" {
		t.Fatalf("明文 HTTP 地址不该生效：%q", cfg.SynthIDURL)
	}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("超过上传上限的图不该送检")
	}))
	defer server.Close()
	plugin := NewAIImageDetectPlugin(server.Client())
	cfg = aiImageDetectConfigFromSettings(SettingValues{aiImageDetectSettingSynthIDURL: server.URL, aiImageDetectSettingMaxUploadMB: 1})
	big := withPNGChunk(aiImageTestPNG(t), "tEXt", append([]byte("Software\x00NovelAI "), bytes.Repeat([]byte("x"), 2<<20)...))
	report := plugin.detect(context.Background(), cfg, big)
	if report.SynthID.Checked || !strings.Contains(report.SynthID.Error, "上传上限") {
		t.Fatalf("synthid = %#v", report.SynthID)
	}
	if report.Verdict != aiImageVerdictMarked {
		t.Fatalf("本地检测结论丢了：%#v", report)
	}
}
