// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package vectorize

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"regexp"
	"strings"
	"testing"
)

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

var (
	white = color.RGBA{255, 255, 255, 255}
	red   = color.RGBA{220, 30, 30, 255}
	blue  = color.RGBA{30, 60, 220, 255}
)

func TestSVGLayersLargestFirst(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	fill(img, img.Rect, white)
	fill(img, image.Rect(20, 10, 100, 70), red)
	fill(img, image.Rect(50, 30, 70, 50), blue) // 红块里的洞
	svg, err := SVG(img, Options{Colors: 3})
	if err != nil {
		t.Fatal(err)
	}
	text := string(svg)
	if !strings.HasPrefix(text, `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80" viewBox="0 0 120 80">`) {
		t.Fatalf("header: %s", text[:120])
	}
	fills := regexp.MustCompile(`color="(#[0-9a-f]{6})"`).FindAllStringSubmatch(text, -1)
	if len(fills) != 3 {
		t.Fatalf("want 3 layers, got %d:\n%s", len(fills), text)
	}
	if fills[0][1] != "#ffffff" || fills[1][1] != "#dc1e1e" || fills[2][1] != "#1e3cdc" {
		t.Fatalf("layers must go from the enclosing shape inwards: %v", fills)
	}
	again, _ := SVG(img, Options{Colors: 3})
	if !bytes.Equal(svg, again) {
		t.Fatal("same image must give the same SVG")
	}
}

// 外轮廓围出的面积要等于区域像素数（没有洞时）；只靠斜角相接的像素是另一块。
func TestTraceOuterMatchesPixels(t *testing.T) {
	shape := []string{
		"..........",
		".###......",
		".###...#..",
		".#######..",
		"...##.....",
		"....#.....",
		".....#....", // 只有斜角相接
		"..........",
	}
	width, height := len(shape[0]), len(shape)
	labels := make([]int16, width*height)
	for y, row := range shape {
		for x, c := range row {
			if c == '#' {
				labels[y*width+x] = 1
			}
		}
	}
	grid := labelComponents(labels, width, height)
	var sizes []int
	for i, comp := range grid.comps {
		if comp.label != 1 {
			continue
		}
		sizes = append(sizes, comp.size)
		outline := grid.traceOuter(int32(i), comp.first)
		if area := math.Abs(polygonArea(outline)); area != float64(comp.size) {
			t.Fatalf("outline area %v, pixels %d, outline %v", area, comp.size, outline)
		}
	}
	if len(sizes) != 2 || sizes[0] != 17 || sizes[1] != 1 {
		t.Fatalf("diagonal pixel should be its own component: %v", sizes)
	}
}

// 斜台阶围成的环，环内的颜色不能从斜角漏出去跟环外连成一块。
func TestDiagonalRingKeepsInsideSeparate(t *testing.T) {
	shape := []string{
		"........",
		"...##...",
		"..#..#..",
		".#....#.",
		".#....#.",
		"..#..#..",
		"...##...",
		"........",
	}
	width, height := len(shape[0]), len(shape)
	labels := make([]int16, width*height)
	for y, row := range shape {
		for x, c := range row {
			if c == '#' {
				labels[y*width+x] = 1
			}
		}
	}
	grid := labelComponents(labels, width, height)
	inside, outside := grid.ids[3*width+3], grid.ids[0]
	if inside == outside {
		t.Fatal("inside of a diagonal ring leaked into the outside")
	}
}

func TestSpecklesMergeIntoNeighbour(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 60, 60))
	fill(img, img.Rect, white)
	fill(img, image.Rect(30, 30, 32, 32), red) // 4 个像素的噪点
	svg, err := SVG(img, Options{Colors: 2, MinArea: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(svg), "<path") != 1 {
		t.Fatalf("speckle should be merged away:\n%s", svg)
	}
}

func TestSVGDownscalesButKeepsDisplaySize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	fill(img, img.Rect, blue)
	svg, err := SVG(img, Options{MaxSide: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(svg), `width="400" height="200" viewBox="0 0 100 50"`) {
		t.Fatalf("header: %s", svg)
	}
}

// 透明底上的半透明深色细线按白底混色，不能描成实心深色。
func TestSemiTransparentBlendsOntoWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 10; y < 30; y++ {
		for x := 10; x < 30; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 64})
		}
	}
	svg, err := SVG(img, Options{Colors: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(svg), `color="#bfbfbf"`) || strings.Count(string(svg), "<path") != 1 {
		t.Fatalf("a 25%% black square on transparent should become one light grey shape:\n%s", svg)
	}
}

func TestSVGRejectsTransparentImage(t *testing.T) {
	if _, err := SVG(image.NewRGBA(image.Rect(0, 0, 10, 10)), Options{}); err == nil {
		t.Fatal("fully transparent image accepted")
	}
}
