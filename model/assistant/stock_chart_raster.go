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

// 走势图只画日线收盘价折线，和关系图、词云共用中文字体与画布原语。
// 配色按国内习惯红涨绿跌，港美股也一样，群友多半看 A 股。
const (
	stockChartWidth   = 900
	stockChartHeight  = 520
	stockChartPadLeft = 40
	stockChartPadAxis = 90
	stockChartTop     = 130
	stockChartBottom  = stockChartHeight - 50
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
	canvas.text(fmt.Sprintf("%s · 近 %d 个交易日收盘（前复权） · %s", quote.Market, len(bars), quote.Time),
		stockChartPadLeft, 116, subtitleFace, relationColorSubtitle, textAlignLeft)

	low, high := bars[0].Close, bars[0].Close
	for _, bar := range bars {
		low, high = math.Min(low, bar.Close), math.Max(high, bar.Close)
	}
	if high == low {
		high, low = high+1, low-1
	}
	margin := (high - low) * 0.08
	low, high = low-margin, high+margin
	left, right := float64(stockChartPadLeft), float64(stockChartWidth-stockChartPadAxis)
	top, bottom := float64(stockChartTop), float64(stockChartBottom)
	pointX := func(index int) float64 { return left + (right-left)*float64(index)/float64(len(bars)-1) }
	pointY := func(value float64) float64 { return bottom - (bottom-top)*(value-low)/(high-low) }

	for step := 0; step <= 4; step++ {
		value := low + (high-low)*float64(step)/4
		y := pointY(value)
		canvas.strokeLine(left, y, right, y, 1, stockColorGrid)
		canvas.text(formatStockPrice(value), right+10, y+5, axisFace, relationColorSubtitle, textAlignLeft)
	}
	for index := 1; index < len(bars); index++ {
		canvas.strokeLine(pointX(index-1), pointY(bars[index-1].Close), pointX(index), pointY(bars[index].Close), 2.5, lineColor)
	}
	canvas.fillCircle(pointX(len(bars)-1), pointY(bars[len(bars)-1].Close), 5, lineColor)
	canvas.text(bars[0].Date, left, bottom+30, axisFace, relationColorSubtitle, textAlignLeft)
	canvas.text(bars[len(bars)-1].Date, right, bottom+30, axisFace, relationColorSubtitle, textAlignRight)

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
