// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// 词云只走进程内栅格化这一条路：它就是一堆横排的字，用不着浏览器排版。和关系图
// 共用中文字体与画布原语；网页端的布局（GroupWordCloudChart.vue）也是同一套螺线，
// 两边对不齐不要紧，但改了配色记得一起改。
const (
	wordCloudImageWidth  = 1000
	wordCloudImageHeight = 720
	wordCloudMinFont     = 16
	wordCloudMaxFont     = 92
)

var wordCloudPalette = []color.NRGBA{
	relationColorCenter,
	relationColorTitle,
	relationColorSubtitle,
	{R: 0xa8, G: 0x3f, B: 0x6c, A: 0xff},
	{R: 0x6b, G: 0x4e, B: 0x63, A: 0xff},
}

// RenderGroupWordCloudPNG 把词云画成 PNG。找不到中文字体时返回错误。
func RenderGroupWordCloudPNG(cloud GroupWordCloud, title, rangeLabel string) ([]byte, error) {
	sfntFont, _, err := LoadCJKFont()
	if err != nil {
		return nil, err
	}
	faces := map[int]font.Face{}
	defer func() {
		for _, face := range faces {
			_ = face.Close()
		}
	}()
	faceFor := func(size int) (font.Face, error) {
		if face, ok := faces[size]; ok {
			return face, nil
		}
		face, err := opentype.NewFace(sfntFont, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, fmt.Errorf("创建 %dpx 字体：%w", size, err)
		}
		faces[size] = face
		return face, nil
	}
	titleFace, err := faceFor(relationTitleFontSize)
	if err != nil {
		return nil, err
	}
	subtitleFace, err := faceFor(relationSubtitleFontSize)
	if err != nil {
		return nil, err
	}

	canvas := newRasterCanvas(wordCloudImageWidth, wordCloudImageHeight, relationColorBackground)
	canvas.text(title, 40, 66, titleFace, relationColorTitle, textAlignLeft)
	canvas.text(fmt.Sprintf("%s · %d 条发言 · %d 人 · 字越大说得越多", rangeLabel, cloud.Messages, cloud.Speakers),
		40, 94, subtitleFace, relationColorSubtitle, textAlignLeft)

	words := cloud.Words
	if len(words) == 0 {
		return nil, fmt.Errorf("没有可画的词")
	}
	maxCount, minCount := words[0].Count, words[len(words)-1].Count
	top, bottom := float64(relationHeaderHeight), float64(wordCloudImageHeight-16)
	centerX, centerY := float64(wordCloudImageWidth)/2, (top+bottom)/2
	var boxes [][4]float64
	for index, word := range words {
		ratio := 1.0
		if maxCount != minCount {
			ratio = math.Sqrt(float64(word.Count-minCount) / float64(maxCount-minCount))
		}
		size := int(math.Round(wordCloudMinFont + (wordCloudMaxFont-wordCloudMinFont)*ratio))
		face, err := faceFor(size)
		if err != nil {
			return nil, err
		}
		width := float64(font.MeasureString(face, word.Text))/64 + 8
		height := float64(size) * 1.1
		for step := 0; step < 3000; step++ {
			angle := float64(step) * 0.2
			radius := 2.4 * angle
			x := centerX + radius*math.Cos(angle)*1.5
			y := centerY + radius*math.Sin(angle)
			box := [4]float64{x - width/2, y - height/2, x + width/2, y + height/2}
			if box[0] < 16 || box[1] < top || box[2] > wordCloudImageWidth-16 || box[3] > bottom {
				continue
			}
			if wordCloudOverlaps(box, boxes) {
				continue
			}
			boxes = append(boxes, box)
			// 基线落在框中线往下约三分之一字高，中文看起来才居中。
			canvas.text(word.Text, x, y+float64(size)*0.36, face, wordCloudPalette[index%len(wordCloudPalette)], textAlignCenter)
			break
		}
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas.img); err != nil {
		return nil, fmt.Errorf("编码词云 PNG：%w", err)
	}
	return buffer.Bytes(), nil
}

func wordCloudOverlaps(box [4]float64, placed [][4]float64) bool {
	for _, other := range placed {
		if box[0] < other[2] && box[2] > other[0] && box[1] < other[3] && box[3] > other[1] {
			return true
		}
	}
	return false
}
