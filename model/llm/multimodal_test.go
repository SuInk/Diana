// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"net/url"
	"strings"
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

func TestDataImageInputTrimsJPEGIntegrityTrailer(t *testing.T) {
	canonical, dataURL, canonicalURL := jpegDataURLWithIntegrityTrailer(t)

	input, ok := dataImageInput(dataURL)
	if !ok {
		t.Fatal("JPEG data URL should be accepted")
	}
	if !bytes.Equal(input.Data, canonical) {
		t.Fatalf("provider bytes retain JPEG trailer: got %d bytes, want %d", len(input.Data), len(canonical))
	}
	if input.EncodedData != base64.StdEncoding.EncodeToString(canonical) {
		t.Fatal("provider base64 was not rebuilt from the canonical JPEG")
	}
	if got := canonicalModelImageURL(dataURL); got != canonicalURL {
		t.Fatal("OpenAI-compatible image URL was not rebuilt from the canonical JPEG")
	}
}

// Clean images are the common case and run through this path on every request and retry, so the
// URL must come back as the very same string instead of a decoded and re-encoded copy.
func TestCanonicalModelImageURLLeavesCleanImagesUntouched(t *testing.T) {
	canonical, _, canonicalURL := jpegDataURLWithIntegrityTrailer(t)
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	for name, value := range map[string]string{
		"clean jpeg":          canonicalURL,
		"unpadded clean jpeg": "data:image/jpeg;base64," + base64.RawStdEncoding.EncodeToString(canonical),
		"png":                 "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"unpadded png":        "data:image/png;base64," + base64.RawStdEncoding.EncodeToString(png),
		"remote":              "https://example.com/a.jpg",
	} {
		if got := canonicalModelImageURL(value); got != value {
			t.Errorf("%s: URL was rewritten", name)
		}
	}
}

func TestCanonicalModelImageURLTrimsTrailerInAnyEncoding(t *testing.T) {
	canonical, dataURL, canonicalURL := jpegDataURLWithIntegrityTrailer(t)
	_, payload, _ := strings.Cut(dataURL, ",")
	withTrailer, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(withTrailer) <= len(canonical) {
		t.Fatal("test JPEG has no trailer")
	}
	for name, value := range map[string]string{
		"url-safe": "data:image/jpeg;base64," + base64.RawURLEncoding.EncodeToString(withTrailer),
		"escaped":  "data:image/jpeg;base64," + url.QueryEscape(payload),
		"wrapped":  "data:image/jpeg;base64,\n" + payload,
	} {
		if got := canonicalModelImageURL(value); got != canonicalURL {
			t.Errorf("%s: JPEG trailer was not trimmed", name)
		}
	}
}

func jpegDataURLWithIntegrityTrailer(t *testing.T) ([]byte, string, string) {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	canvas.Set(1, 0, color.RGBA{G: 255, A: 255})
	canvas.Set(0, 1, color.RGBA{B: 255, A: 255})
	canvas.Set(1, 1, color.White)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	canonical := encoded.Bytes()
	if !bytes.HasSuffix(canonical, []byte{0xff, 0xd9}) {
		t.Fatal("test JPEG has no EOI marker")
	}

	// Mirrors the production failure: an eight-byte proprietary header followed by the MD5 of
	// the complete JPEG stream. The trailer is valid metadata to its producer but is not JPEG.
	digest := md5.Sum(canonical)
	withTrailer := append(append(append([]byte(nil), canonical...),
		0x13, 0x36, 0x06, 0x01, 0x00, 0x00, 0x00, 0x00), digest[:]...)
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(withTrailer)
	canonicalURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(canonical)
	return canonical, dataURL, canonicalURL
}

func TestJPEGStreamEndSkipsEOIMarkerInsideMetadata(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, nil); err != nil {
		t.Fatal(err)
	}
	canonical := encoded.Bytes()
	// Insert a COM segment whose payload contains FF D9. A byte search would mistake it for the
	// image boundary; marker-aware parsing must continue to the real EOI.
	comment := []byte{0xff, 0xfe, 0x00, 0x04, 0xff, 0xd9}
	withComment := append(append(append([]byte(nil), canonical[:2]...), comment...), canonical[2:]...)
	withTrailer := append(append([]byte(nil), withComment...), 0xaa, 0xbb, 0xcc)

	end, ok := jpegStreamEnd(withTrailer)
	if !ok {
		t.Fatal("JPEG boundary was not found")
	}
	if end != len(withComment) {
		t.Fatalf("JPEG end = %d, want %d", end, len(withComment))
	}
	if got := canonicalModelImageBytes(withTrailer); !bytes.Equal(got, withComment) {
		t.Fatal("metadata EOI bytes caused an incorrect trim")
	}
}
