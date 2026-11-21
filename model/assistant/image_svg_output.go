// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/vectorize"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// dianaImageFormatSVG 不是图片接口的格式：照常出一张 PNG，再在本地描成 SVG 文件，
// 图和文件一起发。来自群友要 SVG 画：模型手写的 SVG 只能拼几何图形，生图再描摹
// 观感好得多（2026-11-21 实测）。
const dianaImageFormatSVG = "svg"

type dianaImageSVGFile struct {
	Name string
	Data []byte
}

// upstreamImageFormat 是交给图片接口的成品格式；svg 要先拿到位图。
func upstreamImageFormat(format string) string {
	if format == dianaImageFormatSVG {
		return llm.ImageFormatPNG
	}
	return format
}

// vectorizeGeneratedImages 把成图逐张描成 SVG。描不出来的那张跳过，原因带回给用户：
// 图已经画好了，不能因为转换失败整个任务算失败。
func (r *Runtime) vectorizeGeneratedImages(ctx context.Context, stem string, images []string) ([]dianaImageSVGFile, string) {
	if stem == "" {
		stem = "image-" + time.Now().Format("20060102-150405")
	}
	files := make([]dianaImageSVGFile, 0, len(images))
	var failures []string
	for index, raw := range images {
		data, err := loadGeneratedImage(ctx, raw)
		if err == nil {
			var decoded image.Image
			if decoded, _, err = image.Decode(bytes.NewReader(data)); err == nil {
				data, err = vectorize.SVG(decoded, vectorize.Options{})
			}
		}
		if err != nil {
			log.Printf("diana image: 转 SVG 失败：%v", err)
			failures = append(failures, err.Error())
			continue
		}
		name := stem
		if len(images) > 1 {
			name = fmt.Sprintf("%s-%d", stem, index+1)
		}
		files = append(files, dianaImageSVGFile{Name: name + ".svg", Data: data})
	}
	note := ""
	if len(failures) > 0 {
		note = "SVG 没转成：" + strings.Join(failures, "；")
	}
	return files, note
}

// sendGeneratedSVGFiles 把描好的 SVG 当文件发出去，并按成图的规矩存进工作目录。
// 返回要补告诉用户的话，全部发出时为空。
func (r *Runtime) sendGeneratedSVGFiles(ctx context.Context, event MessageEvent, stem string, files []dianaImageSVGFile) string {
	var failures []string
	now := time.Now()
	for _, file := range files {
		if err := r.sendFileAttachment(ctx, event, file.Name, file.Data); err != nil {
			log.Printf("diana image: 发送 SVG 文件失败：%v", err)
			failures = append(failures, file.Name)
		}
		if stem == "" {
			continue
		}
		saved, err := r.saveWorkspacePayload(event, workspaceMediaPayload{data: file.Data, kind: "file", generated: true}, agent.WorkspaceOutputsDir+"/"+file.Name, false, now)
		if err != nil {
			log.Printf("diana image: SVG 存进工作目录失败：%v", err)
			continue
		}
		log.Printf("diana image: SVG 已存进工作目录 %s", saved.Path)
	}
	if len(failures) == 0 {
		return ""
	}
	return "SVG 文件没发出去（" + strings.Join(failures, "、") + "）"
}

// joinDianaImageNotes 把几段随图说明接成一句，空的跳过。
func joinDianaImageNotes(notes ...string) string {
	kept := make([]string, 0, len(notes))
	for _, note := range notes {
		if note = strings.TrimSpace(note); note != "" {
			kept = append(kept, note)
		}
	}
	return strings.Join(kept, "\n")
}
