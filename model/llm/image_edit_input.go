// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"fmt"
	"image/gif"
	"image/png"
	"net/http"
	"path/filepath"
	"strings"
)

// maxImageEditGIFPixels 是抽帧前允许的 GIF 画布像素上限，防止一张声明巨大尺寸的
// GIF 在解码时吃光内存。
const maxImageEditGIFPixels = 80_000_000

// normalizeImageEditInput 把源图整理成改图接口收得下的样子。
//
// 图片接口只收 PNG、JPEG、WebP，动图 GIF 会被直接判成 invalid_image_file。
// 群里拿来改的表情包大多是 GIF，这里取第一帧转成 PNG。另外按实际字节纠正
// MIME：下载地址或缓存文件的扩展名经常和内容对不上，照着扩展名报类型同样会被拒。
func normalizeImageEditInput(input imageEditInputData, index int) (imageEditInputData, error) {
	detected := http.DetectContentType(input.data)
	if detected != "image/gif" {
		if strings.HasPrefix(detected, "image/") && detected != input.mediaType {
			input.mediaType = detected
			input.filename = imageEditFilename(strings.TrimSuffix(input.filename, filepath.Ext(input.filename)), detected, index)
		}
		return input, nil
	}
	config, err := gif.DecodeConfig(bytes.NewReader(input.data))
	if err != nil {
		return imageEditInputData{}, fmt.Errorf("llm: decode gif image edit input: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxImageEditGIFPixels {
		return imageEditInputData{}, fmt.Errorf("llm: gif image edit input is %dx%d, too large to convert", config.Width, config.Height)
	}
	frame, err := gif.Decode(bytes.NewReader(input.data))
	if err != nil {
		return imageEditInputData{}, fmt.Errorf("llm: decode gif image edit input: %w", err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, frame); err != nil {
		return imageEditInputData{}, fmt.Errorf("llm: convert gif image edit input to png: %w", err)
	}
	base := strings.TrimSuffix(input.filename, filepath.Ext(input.filename))
	return imageEditInputData{
		data:      encoded.Bytes(),
		mediaType: "image/png",
		filename:  imageEditFilename(base, "image/png", index),
	}, nil
}
