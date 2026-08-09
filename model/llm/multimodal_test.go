package llm

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// assistant 侧的 MediaStore 生成的就是这种 data URL；两边格式必须对得上，
// 否则识图会静默退化成「把地址原样交给服务商」。
func TestDataImageInputAcceptsMediaStoreFormat(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)

	input, ok := imageInputFromURL(dataURL)
	if !ok {
		t.Fatal("MediaStore 生成的 data URL 应能被解析")
	}
	if input.MediaType != "image/png" {
		t.Fatalf("媒体类型错误：%q", input.MediaType)
	}
	if !bytes.Equal(input.Data, raw) {
		t.Fatal("解码出的字节与原图不一致")
	}
	if input.EncodedData == "" {
		t.Fatal("EncodedData 为空会让 Anthropic 分支回落到 URL 提交")
	}
	// 走 URL 的分支必须留空，否则 provider 会同时拿到两种来源。
	if input.URL != "" {
		t.Fatalf("base64 输入不该带 URL，实际 %q", input.URL)
	}
}
