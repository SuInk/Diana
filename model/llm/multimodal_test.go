package llm

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// MediaStore produces this exact data URL format. Providers must decode it
// instead of silently forwarding the URL text as an external image.
func TestDataImageInputAcceptsMediaStoreFormat(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)

	input, ok := imageInputFromURL(dataURL)
	if !ok {
		t.Fatal("MediaStore data URL should be accepted")
	}
	if input.MediaType != "image/png" {
		t.Fatalf("media type = %q", input.MediaType)
	}
	if !bytes.Equal(input.Data, raw) {
		t.Fatal("decoded bytes differ from source image")
	}
	if input.EncodedData == "" {
		t.Fatal("EncodedData is required by the Anthropic image branch")
	}
	if input.URL != "" {
		t.Fatalf("base64 input should not retain URL, got %q", input.URL)
	}
}
