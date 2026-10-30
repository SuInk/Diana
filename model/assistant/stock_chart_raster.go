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
	"golang.org/x/image/vector"
)

// 走势图画日 K 线（蜡烛图）加底部成交量柱，和关系图、词云共用中文字体与画布原语。
// 配色按国内习惯红涨绿跌，港美股也一样，群友多半看 A 股。
const (
	stockChartWidth   = 900
	stockChartHeight  = 600
	stockChartPadLeft = 40
	stockChartPadAxis = 90
	stockChartTop     = 130
	stockChartBottom  = 430
	// 成交量区在 K 线区下面，和它隔一条缝。
	stockVolumeTop    = 450
	stockVolumeBottom = stockChartHeight - 50
	stockChartFont    = 15
)

var (
	stockColorUp   = color.NRGBA{R: 0xe0, G: 0x3e, B: 0x3e, A: 0xff}
	stockColorDown = color.NRGBA{R: 0x1f, G: 0x9d, B: 0x55, A: 0xff}
	stockColorGrid = color.NRGBA{R: 0xec, G: 0xe6, B: 0xea, A: 0xff}
)

// RenderStockChartPNG 把报价和日线画成 PNG。日线少于两根或找不到中文字体时返回错误。
func RenderStockChartPNG(quote StockQuote, bars []StockBar) ([]byte, error) {
	if len(bars) < 2 {
		return nil, fmt.Errorf("日线不足，画不了走势")
	}
	sfntFont, _, err := LoadCJKFont()
	if err != nil {
		return nil, err
	}
	newFace := func(size float64) (font.Face, error) {
		return opentype.NewFace(sfntFont, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	}
	titleFace, err := newFace(relationTitleFontSize)
	if err != nil {
		return nil, err
	}
	defer titleFace.Close()
	subtitleFace, err := newFace(relationSubtitleFontSize)
	if err != nil {
		return nil, err
	}
	defer subtitleFace.Close()
	axisFace, err := newFace(stockChartFont)
	if err != nil {
		return nil, err
	}
	defer axisFace.Close()

	lineColor := stockColorUp
	if quote.Change < 0 {
		lineColor = stockColorDown
	}
	canvas := newRasterCanvas(stockChartWidth, stockChartHeight, relationColorBackground)
	canvas.text(fmt.Sprintf("%s  %s", quote.Name, quote.Symbol), stockChartPadLeft, 56, titleFace, relationColorTitle, textAlignLeft)
	canvas.text(fmt.Sprintf("%s %s  %+.2f  %+.2f%%", formatStockPrice(quote.Price), quote.Currency, quote.Change, quote.ChangePct),
		stockChartPadLeft, 90, titleFace, lineColor, textAlignLeft)
	canvas.text(fmt.Sprintf("%s · 近 %d 个交易日日 K（前复权） · %s", quote.Market, len(bars), quote.Time),
		stockChartPadLeft, 116, subtitleFace, relationColorSubtitle, textAlignLeft)

	low, high, maxVolume := bars[0].Low, bars[0].High, 0.0
	for _, bar := range bars {
		low, high, maxVolume = math.Min(low, bar.Low), math.Max(high, bar.High), math.Max(maxVolume, bar.Volume)
	}
	if high == low {
		high, low = high+1, low-1
	}
	margin := (high - low) * 0.06
	low, high = low-margin, high+margin
	left, right := float64(stockChartPadLeft), float64(stockChartWidth-stockChartPadAxis)
	top, bottom := float64(stockChartTop), float64(stockChartBottom)
	slot := (right - left) / float64(len(bars))
	bodyWidth := math.Max(1, slot*0.7)
	centerX := func(index int) float64 { return left + slot*(float64(index)+0.5) }
	pointY := func(value float64) float64 { return bottom - (bottom-top)*(value-low)/(high-low) }

	for step := 0; step <= 4; step++ {
		value := low + (high-low)*float64(step)/4
		y := pointY(value)
		canvas.strokeLine(left, y, right, y, 1, stockColorGrid)
		canvas.text(formatStockPrice(value), right+10, y+5, axisFace, relationColorSubtitle, textAlignLeft)
	}
	canvas.strokeLine(left, stockVolumeBottom, right, stockVolumeBottom, 1, stockColorGrid)
	canvas.text("成交量", right+10, stockVolumeTop+14, axisFace, relationColorSubtitle, textAlignLeft)
	for index, bar := range bars {
		barColor := stockColorUp
		if bar.Close < bar.Open {
			barColor = stockColorDown
		}
		x := centerX(index)
		canvas.strokeLine(x, pointY(bar.High), x, pointY(bar.Low), 1.2, barColor)
		bodyTop, bodyBottom := pointY(math.Max(bar.Open, bar.Close)), pointY(math.Min(bar.Open, bar.Close))
		// 开收相同的十字星也要留一条看得见的横线。
		if bodyBottom-bodyTop < 1.5 {
			bodyBottom = bodyTop + 1.5
		}
		canvas.fillRect(x-bodyWidth/2, bodyTop, x+bodyWidth/2, bodyBottom, barColor)
		if maxVolume > 0 {
			volumeTop := stockVolumeBottom - (stockVolumeBottom-stockVolumeTop)*bar.Volume/maxVolume
			canvas.fillRect(x-bodyWidth/2, volumeTop, x+bodyWidth/2, stockVolumeBottom, withAlpha(barColor, 0.55))
		}
	}

	canvas.text(bars[0].Date, left, stockVolumeBottom+30, axisFace, relationColorSubtitle, textAlignLeft)
	canvas.text(bars[len(bars)-1].Date, right, stockVolumeBottom+30, axisFace, relationColorSubtitle, textAlignRight)

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas.img); err != nil {
		return nil, fmt.Errorf("编码走势图 PNG：%w", err)
	}
	return buffer.Bytes(), nil
}

// formatStockPrice 价格按量级给小数位：港股仙股要三位，大票两位就够。
func formatStockPrice(value float64) string {
	if math.Abs(value) < 10 {
		return fmt.Sprintf("%.3f", value)
	}
	return fmt.Sprintf("%.2f", value)
}

// fillRect 填一个轴对齐矩形，K 线实体和成交量柱都用它。
func (c *rasterCanvas) fillRect(x1, y1, x2, y2 float64, fill color.NRGBA) {
	c.fillPath(func(r *vector.Rasterizer) {
		r.MoveTo(float32(x1), float32(y1))
		r.LineTo(float32(x2), float32(y1))
		r.LineTo(float32(x2), float32(y2))
		r.LineTo(float32(x1), float32(y2))
		r.ClosePath()
	}, fill)
}
