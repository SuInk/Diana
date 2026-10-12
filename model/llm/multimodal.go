// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
	"mime"
	"net/url"
	"path"
	"strings"
)

const defaultImageMediaType = "image/jpeg"

type imageInput struct {
	URL         string
	MediaType   string
	Data        []byte
	EncodedData string
}

func imageInputFromURL(value string) (imageInput, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return imageInput{}, false
	}
	if input, ok := dataImageInput(value); ok {
		return input, true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return imageInput{}, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return imageInput{}, false
	}
	return imageInput{
		URL:       value,
		MediaType: imageMediaTypeFromPath(parsed.Path),
	}, true
}

func dataImageInput(value string) (imageInput, bool) {
	mediaType, data, ok := decodeDataImageURL(value)
	if !ok {
		return imageInput{}, false
	}
	data = canonicalModelImageBytes(data)
	return imageInput{
		MediaType:   mediaType,
		Data:        data,
		EncodedData: base64.StdEncoding.EncodeToString(data),
	}, true
}

func decodeDataImageURL(value string) (string, []byte, bool) {
	rest, ok := strings.CutPrefix(value, "data:")
	if !ok {
		return "", nil, false
	}
	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", nil, false
	}
	headerParts := strings.Split(header, ";")
	mediaType := strings.ToLower(strings.TrimSpace(headerParts[0]))
	if !strings.HasPrefix(mediaType, "image/") {
		return "", nil, false
	}
	base64Encoded := false
	for _, part := range headerParts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded {
		return "", nil, false
	}
	unescaped, err := url.PathUnescape(payload)
	if err != nil {
		return "", nil, false
	}
	data, ok := decodeBase64Image(unescaped)
	if !ok {
		return "", nil, false
	}
	return mediaType, data, true
}

// canonicalModelImageURL rebuilds a data URL only when canonicalModelImageBytes actually removed
// a trailer. Every other image is returned untouched: this runs for each image of each request
// and retry, so clean images must not pay for a decode and re-encode round trip.
func canonicalModelImageURL(value string) string {
	value = strings.TrimSpace(value)
	if !dataURLMayHoldJPEG(value) {
		return value
	}
	mediaType, data, ok := decodeDataImageURL(value)
	if !ok {
		return value
	}
	canonical := canonicalModelImageBytes(data)
	if len(canonical) == len(data) {
		return value
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(canonical)
}

// dataURLMayHoldJPEG reports whether a data URL payload could decode to bytes starting with
// JPEG's FF D8. Those always encode to a leading "/" ("_" in the URL-safe alphabet); "%" and
// whitespace are kept as possible because the payload may be escaped or wrapped.
func dataURLMayHoldJPEG(value string) bool {
	if !strings.HasPrefix(value, "data:") {
		return false
	}
	_, payload, ok := strings.Cut(value, ",")
	return ok && payload != "" && strings.IndexByte("/_% \t\r\n", payload[0]) >= 0
}

func decodeBase64Image(value string) ([]byte, bool) {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, value)
	if value == "" {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		data, err := encoding.DecodeString(value)
		if err == nil {
			return data, true
		}
	}
	return nil, false
}

// canonicalModelImageBytes removes bytes after a complete JPEG stream before the image is
// submitted to a provider. Some chat platforms preserve proprietary integrity trailers after
// JPEG's EOI marker. Desktop decoders commonly ignore those trailers, while stricter multimodal
// APIs reject the entire otherwise-readable image. The caller decoded a fresh data URL buffer, so
// slicing it here never changes the cached original or its content hash.
func canonicalModelImageBytes(data []byte) []byte {
	end, ok := jpegStreamEnd(data)
	if !ok || end == len(data) {
		return data
	}
	// Only repair a stream whose JPEG header remains readable at the parsed boundary. If marker
	// parsing found a false boundary in malformed input, preserve the original for the provider to
	// reject instead of silently changing it.
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data[:end])); err != nil {
		return data
	}
	return data[:end]
}

// jpegStreamEnd returns the byte immediately after the structural EOI marker. It walks marker
// segments instead of searching for the last FF D9 pair: metadata and arbitrary trailer bytes may
// contain the same pair, and entropy-coded scan data has its own byte-stuffing rules.
func jpegStreamEnd(data []byte) (int, bool) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0, false
	}

	position := 2
	inScan := false
	for position < len(data) {
		if inScan && data[position] != 0xff {
			position++
			continue
		}
		if data[position] != 0xff {
			return 0, false
		}

		// FF fill bytes may appear before a marker.
		for position < len(data) && data[position] == 0xff {
			position++
		}
		if position >= len(data) {
			return 0, false
		}
		marker := data[position]
		position++

		if marker == 0x00 { // Stuffed FF inside entropy-coded scan data.
			if !inScan {
				return 0, false
			}
			continue
		}
		if marker == 0xd9 { // EOI
			return position, true
		}
		if marker == 0xd8 { // A second SOI cannot occur inside one JPEG stream.
			return 0, false
		}
		if marker >= 0xd0 && marker <= 0xd7 { // Restart marker inside scan data.
			if !inScan {
				return 0, false
			}
			continue
		}
		if marker == 0x01 { // TEM is the only other standalone marker.
			continue
		}

		resumeScan := inScan && marker == 0xdc // DNL may appear inside scan data.
		inScan = false
		if position+2 > len(data) {
			return 0, false
		}
		segmentLength := int(data[position])<<8 | int(data[position+1])
		if segmentLength < 2 || position+segmentLength > len(data) {
			return 0, false
		}
		position += segmentLength
		inScan = marker == 0xda || resumeScan // SOS starts entropy-coded scan data.
	}
	return 0, false
}

func imageMediaTypeFromPath(filePath string) string {
	ext := strings.ToLower(path.Ext(filePath))
	if ext == ".jpg" {
		ext = ".jpeg"
	}
	if ext != "" {
		if mediaType := mime.TypeByExtension(ext); strings.HasPrefix(mediaType, "image/") {
			if value, _, ok := strings.Cut(mediaType, ";"); ok {
				return strings.TrimSpace(value)
			}
			return mediaType
		}
	}
	switch ext {
	case ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return defaultImageMediaType
	}
}
