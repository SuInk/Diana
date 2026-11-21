// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package vectorize 把一张位图描成彩色 SVG，供「先出图再转矢量」用。
//
// 做法跟 vtracer 的 stacked 模式同一路数：先把颜色压到有限的调色板，按颜色找
// 4 连通区域，碎点并进相邻的大块；每块只描外轮廓，按外轮廓面积从大到小叠着画，
// 洞里的内容是更小的块、画在后面自然盖上，所以不用处理带洞的路径。轮廓先用
// Douglas–Peucker 去掉像素台阶，再把不是拐角的点连成三次贝塞尔曲线。
//
// 纯 Go、不引第三方依赖：Diana 是单个二进制跨五个平台发，vtracer 只有 Rust 本体
// 和 Python/Node 绑定，potrace 是 GPL。
package vectorize

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"

	xdraw "golang.org/x/image/draw"
)

// Options 是描摹参数，零值取默认。
type Options struct {
	// Colors 是调色板大小。扁平插画 16～32 够用，越多路径越碎、文件越大。
	Colors int
	// MinArea 是碎点阈值（像素数，按处理后的尺寸算），更小的区域并进相邻区域。
	// 零值按图面积取四万分之一，至少 24；再大会把轮辐间的空白、五官这类细节并掉。
	MinArea int
	// Tolerance 是去台阶时允许偏离原轮廓的像素数。
	Tolerance float64
	// CornerAngle 是拐角阈值（度）：转角大于它的点保持尖角，其余连成曲线。
	CornerAngle float64
	// MaxSide 是处理时长边的上限，超过就先缩小再描；输出仍按原图尺寸显示。
	MaxSide int
}

const (
	defaultColors      = 24
	minMinArea         = 24
	defaultTolerance   = 1.25
	defaultCornerAngle = 60
	defaultMaxSide     = 1536
	kmeansSamples      = 40000
	kmeansIterations   = 12
	// alphaThreshold 以下算透明、不描；其余半透明像素先按白底混色。
	alphaThreshold = 16
)

func (o Options) withDefaults() Options {
	if o.Colors <= 0 {
		o.Colors = defaultColors
	}
	if o.Tolerance <= 0 {
		o.Tolerance = defaultTolerance
	}
	if o.CornerAngle <= 0 {
		o.CornerAngle = defaultCornerAngle
	}
	if o.MaxSide <= 0 {
		o.MaxSide = defaultMaxSide
	}
	return o
}

// SVG 把 img 描成一份完整的 SVG 文档（带 width、height 和 viewBox）。
func SVG(img image.Image, opts Options) ([]byte, error) {
	opts = opts.withDefaults()
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, errors.New("vectorize: 图片是空的")
	}
	pixels := toRGBA(img, opts.MaxSide)
	width, height := pixels.Rect.Dx(), pixels.Rect.Dy()
	palette := kmeansPalette(pixels, opts.Colors)
	if len(palette) == 0 {
		return nil, errors.New("vectorize: 图片全透明")
	}
	labels := assignLabels(pixels, palette)
	grid := labelComponents(labels, width, height)
	minArea := opts.MinArea
	if minArea <= 0 {
		minArea = max(minMinArea, width*height/40000)
	}
	grid.mergeSpeckles(minArea)

	type layer struct {
		color uint8
		area  float64
		path  string
	}
	layers := make([]layer, 0, len(grid.comps))
	cornerCos := math.Cos(opts.CornerAngle * math.Pi / 180)
	for id, comp := range grid.comps {
		if comp.size == 0 {
			continue
		}
		outline := grid.traceOuter(int32(id), comp.first)
		area := math.Abs(polygonArea(outline))
		points := simplifyClosed(outline, opts.Tolerance)
		if len(points) < 3 {
			continue
		}
		layers = append(layers, layer{color: comp.label, area: area, path: smoothPath(points, cornerCos)})
	}
	// 外轮廓面积大的先画；包在别人洞里的块面积一定更小，会画在上面。
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].area > layers[j].area })

	var out bytes.Buffer
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, bounds.Dx(), bounds.Dy(), width, height)
	// 同色描一像素细边：相邻两块各自去台阶后边界不完全重合，不描边会漏出发丝缝。
	// 每块的颜色写在 color 上，填充和描边都取 currentColor，省得每条路径写两遍。
	out.WriteString("\n<g fill=\"currentColor\" stroke=\"currentColor\" stroke-width=\"1\" stroke-linejoin=\"round\">")
	for _, l := range layers {
		c := palette[l.color]
		fmt.Fprintf(&out, "\n"+`<path color="#%02x%02x%02x" d="%s"/>`, c.R, c.G, c.B, l.path)
	}
	out.WriteString("\n</g>\n</svg>\n")
	return out.Bytes(), nil
}

func toRGBA(img image.Image, maxSide int) *image.RGBA {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if longest := max(width, height); longest > maxSide {
		scale := float64(maxSide) / float64(longest)
		width = max(1, int(math.Round(float64(width)*scale)))
		height = max(1, int(math.Round(float64(height)*scale)))
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	if width == bounds.Dx() && height == bounds.Dy() {
		xdraw.Draw(dst, dst.Rect, img, bounds.Min, xdraw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Rect, img, bounds, xdraw.Src, nil)
	}
	// 半透明像素按白底混色。生图接口常给透明底，细线、阴影、光晕是低透明度的深色，
	// 当不透明处理会描成实心的黑块（车轮整个涂黑）；混到白底上才是群聊里看到的样子。
	// image.RGBA 存的是预乘过 alpha 的颜色，加上 255-a 就是白底上的颜色。
	for i := 0; i < len(dst.Pix); i += 4 {
		a := dst.Pix[i+3]
		if a == 255 {
			continue
		}
		white := 255 - a
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2] = dst.Pix[i]+white, dst.Pix[i+1]+white, dst.Pix[i+2]+white
		if a >= alphaThreshold {
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// kmeansPalette 在均匀抽样的不透明像素上跑 k-means（k-means++ 起点，固定种子，
// 同一张图每次结果一样）。
func kmeansPalette(pixels *image.RGBA, k int) []color.RGBA {
	total := len(pixels.Pix) / 4
	stride := max(1, total/kmeansSamples)
	samples := make([][3]float64, 0, total/stride+1)
	for i := 0; i < total; i += stride {
		p := pixels.Pix[i*4 : i*4+4]
		if p[3] < alphaThreshold {
			continue
		}
		samples = append(samples, [3]float64{float64(p[0]), float64(p[1]), float64(p[2])})
	}
	if len(samples) == 0 {
		return nil
	}
	rng := rand.New(rand.NewPCG(1, 2))
	centers := [][3]float64{samples[rng.IntN(len(samples))]}
	nearest := make([]float64, len(samples))
	for i := range nearest {
		nearest[i] = math.Inf(1)
	}
	for len(centers) < k {
		last := centers[len(centers)-1]
		sum := 0.0
		for i, s := range samples {
			nearest[i] = math.Min(nearest[i], dist2(s, last))
			sum += nearest[i]
		}
		if sum == 0 {
			break // 颜色比 k 少，已经全部覆盖
		}
		target := rng.Float64() * sum
		chosen := len(samples) - 1
		for i, d := range nearest {
			target -= d
			if target <= 0 {
				chosen = i
				break
			}
		}
		centers = append(centers, samples[chosen])
	}
	assign := make([]int, len(samples))
	for iter := 0; iter < kmeansIterations; iter++ {
		sums := make([][4]float64, len(centers))
		for i, s := range samples {
			best := nearestCenter(centers, s)
			assign[i] = best
			sums[best][0] += s[0]
			sums[best][1] += s[1]
			sums[best][2] += s[2]
			sums[best][3]++
		}
		for c := range centers {
			if n := sums[c][3]; n > 0 {
				centers[c] = [3]float64{sums[c][0] / n, sums[c][1] / n, sums[c][2] / n}
			}
		}
	}
	centers = mergeCloseCenters(centers, assign)
	palette := make([]color.RGBA, len(centers))
	for i, c := range centers {
		palette[i] = color.RGBA{R: clampByte(c[0]), G: clampByte(c[1]), B: clampByte(c[2]), A: 255}
	}
	return palette
}

// mergeCloseColor 是两种颜色算作同一种的 RGB 欧氏距离。k-means 会把大片底色拆成
// 几档肉眼分不出的浅灰白，描出来就是满屏淡斑点。
const mergeCloseColor = 18

// mergeCloseCenters 把相距不到 mergeCloseColor 的中心并成一个，按样本数加权取均值。
func mergeCloseCenters(centers [][3]float64, assign []int) [][3]float64 {
	counts := make([]float64, len(centers))
	for _, c := range assign {
		counts[c]++
	}
	merged := make([][3]float64, 0, len(centers))
	weights := make([]float64, 0, len(centers))
	for c, center := range centers {
		if counts[c] == 0 {
			continue
		}
		joined := false
		for m := range merged {
			if dist2(merged[m], center) < mergeCloseColor*mergeCloseColor {
				w := weights[m] + counts[c]
				for k := 0; k < 3; k++ {
					merged[m][k] = (merged[m][k]*weights[m] + center[k]*counts[c]) / w
				}
				weights[m] = w
				joined = true
				break
			}
		}
		if !joined {
			merged = append(merged, center)
			weights = append(weights, counts[c])
		}
	}
	return merged
}

func nearestCenter(centers [][3]float64, s [3]float64) int {
	best, bestDist := 0, math.Inf(1)
	for c, center := range centers {
		if d := dist2(s, center); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

func dist2(a, b [3]float64) float64 {
	dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dr*dr + dg*dg + db*db
}

func clampByte(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// noLabel 标记透明像素，不参与描摹。
const noLabel = -1

func assignLabels(pixels *image.RGBA, palette []color.RGBA) []int16 {
	centers := make([][3]float64, len(palette))
	for i, c := range palette {
		centers[i] = [3]float64{float64(c.R), float64(c.G), float64(c.B)}
	}
	total := len(pixels.Pix) / 4
	labels := make([]int16, total)
	// 插画里同一种颜色反复出现，按颜色缓存最近的调色板下标。
	cache := map[uint32]int16{}
	for i := 0; i < total; i++ {
		p := pixels.Pix[i*4 : i*4+4]
		if p[3] < alphaThreshold {
			labels[i] = noLabel
			continue
		}
		key := uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2])
		label, ok := cache[key]
		if !ok {
			label = int16(nearestCenter(centers, [3]float64{float64(p[0]), float64(p[1]), float64(p[2])}))
			cache[key] = label
		}
		labels[i] = label
	}
	return labels
}

type component struct {
	label uint8
	size  int
	first int // 光栅顺序里的第一个像素，也就是最上一行最左边那个
}

type componentGrid struct {
	width, height int
	ids           []int32 // 每个像素所属区域，透明像素为 -1
	comps         []component
}

// 区域按 4 连通算。描出来的细轮廓常是一格一格的斜台阶，8 连通时环内外同色的
// 像素能从斜角「漏」出去连成一片：车轮里的白底并进了背景，先画背景、后画车轮，
// 整个轮子就被涂实了。
var neighbors4 = [4][2]int{{0, -1}, {-1, 0}, {1, 0}, {0, 1}}

func labelComponents(labels []int16, width, height int) *componentGrid {
	grid := &componentGrid{width: width, height: height, ids: make([]int32, len(labels))}
	for i := range grid.ids {
		grid.ids[i] = -1
	}
	stack := make([]int, 0, 1024)
	for start, label := range labels {
		if label == noLabel || grid.ids[start] >= 0 {
			continue
		}
		id := int32(len(grid.comps))
		comp := component{label: uint8(label), first: start}
		grid.ids[start] = id
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			index := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp.size++
			x, y := index%width, index/width
			for _, d := range neighbors4 {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= width || ny >= height {
					continue
				}
				next := ny*width + nx
				if grid.ids[next] < 0 && labels[next] == label {
					grid.ids[next] = id
					stack = append(stack, next)
				}
			}
		}
		grid.comps = append(grid.comps, comp)
	}
	return grid
}

// mergeSpeckles 把小于 minArea 的区域从小到大并进与它接触边最长的相邻区域。
// 抗锯齿边缘和噪点量化后是成片的碎块，不并掉的话路径数和文件体积都会翻好几倍。
func (g *componentGrid) mergeSpeckles(minArea int) {
	members := map[int32][]int{}
	for index, id := range g.ids {
		if id >= 0 && g.comps[id].size < minArea {
			members[id] = append(members[id], index)
		}
	}
	small := make([]int32, 0, len(members))
	for id := range members {
		small = append(small, id)
	}
	sort.Slice(small, func(i, j int) bool {
		a, b := g.comps[small[i]], g.comps[small[j]]
		if a.size != b.size {
			return a.size < b.size
		}
		return small[i] < small[j]
	})
	for _, id := range small {
		comp := &g.comps[id]
		if comp.size == 0 || comp.size >= minArea {
			continue
		}
		contacts := map[int32]int{}
		for _, index := range members[id] {
			x, y := index%g.width, index/g.width
			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= g.width || ny >= g.height {
					continue
				}
				if other := g.ids[ny*g.width+nx]; other >= 0 && other != id {
					contacts[other]++
				}
			}
		}
		target, best := int32(-1), 0
		for other, count := range contacts {
			if count > best || (count == best && other < target) {
				target, best = other, count
			}
		}
		if target < 0 {
			continue
		}
		for _, index := range members[id] {
			g.ids[index] = target
		}
		into := &g.comps[target]
		into.size += comp.size
		into.first = min(into.first, comp.first)
		if into.size < minArea || members[target] != nil {
			members[target] = append(members[target], members[id]...)
		}
		comp.size = 0
		delete(members, id)
	}
}

type point struct{ x, y float64 }

// traceOuter 沿像素角点描区域的外轮廓，区域始终在前进方向右侧（y 轴朝下），
// 只记录拐点。前方两格都在区域里就左转、只有右前方在就直行、右前方不在就右转；
// 只靠斜角相接的像素不算进来，跟 labelComponents 的 4 连通一致。
func (g *componentGrid) traceOuter(id int32, first int) []point {
	inside := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < g.width && y < g.height && g.ids[y*g.width+x] == id
	}
	// 象限 (sx, sy) 相对角点 (x, y) 的那个像素。
	quadrant := func(x, y, sx, sy int) bool {
		px, py := x, y
		if sx < 0 {
			px--
		}
		if sy < 0 {
			py--
		}
		return inside(px, py)
	}
	startX, startY := first%g.width, first/g.width
	x, y, dx, dy := startX, startY, 1, 0
	outline := []point{{float64(x), float64(y)}}
	for steps := 0; steps < 4*(g.width+1)*(g.height+1); steps++ {
		rx, ry := -dy, dx // 右手方向
		ndx, ndy := rx, ry
		if quadrant(x, y, dx+rx, dy+ry) {
			ndx, ndy = dx, dy
			if quadrant(x, y, dx-rx, dy-ry) {
				ndx, ndy = -rx, -ry
			}
		}
		if ndx != dx || ndy != dy {
			if steps > 0 {
				outline = append(outline, point{float64(x), float64(y)})
			}
			dx, dy = ndx, ndy
		}
		x, y = x+dx, y+dy
		if x == startX && y == startY {
			break
		}
	}
	return outline
}

func polygonArea(points []point) float64 {
	area := 0.0
	for i, p := range points {
		q := points[(i+1)%len(points)]
		area += p.x*q.y - q.x*p.y
	}
	return area / 2
}

// simplifyClosed 对闭合多边形做 Douglas–Peucker：从离第一个点最远的点切成两段
// 分别化简。斜线上一格一格的台阶偏离直线不到一个像素，会被拉直；真正的拐角留下。
func simplifyClosed(points []point, tolerance float64) []point {
	if len(points) < 4 {
		return points
	}
	far, farDist := 0, -1.0
	for i, p := range points {
		if d := sqDist(p, points[0]); d > farDist {
			far, farDist = i, d
		}
	}
	first := douglasPeucker(points[:far+1], tolerance)
	second := douglasPeucker(append(append([]point(nil), points[far:]...), points[0]), tolerance)
	result := append(first[:len(first)-1], second[:len(second)-1]...)
	return result
}

func douglasPeucker(points []point, tolerance float64) []point {
	if len(points) < 3 {
		return append([]point(nil), points...)
	}
	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true
	type span struct{ from, to int }
	stack := []span{{0, len(points) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		index, worst := -1, tolerance
		for i := s.from + 1; i < s.to; i++ {
			if d := segmentDistance(points[i], points[s.from], points[s.to]); d > worst {
				index, worst = i, d
			}
		}
		if index >= 0 {
			keep[index] = true
			stack = append(stack, span{s.from, index}, span{index, s.to})
		}
	}
	out := make([]point, 0, len(points))
	for i, p := range points {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func sqDist(a, b point) float64 {
	dx, dy := a.x-b.x, a.y-b.y
	return dx*dx + dy*dy
}

func segmentDistance(p, a, b point) float64 {
	length := sqDist(a, b)
	if length == 0 {
		return math.Sqrt(sqDist(p, a))
	}
	t := math.Max(0, math.Min(1, ((p.x-a.x)*(b.x-a.x)+(p.y-a.y)*(b.y-a.y))/length))
	return math.Sqrt(sqDist(p, point{a.x + t*(b.x-a.x), a.y + t*(b.y-a.y)}))
}

// smoothPath 把化简后的点连成闭合路径。不是拐角的点取前后两点连线方向作切线，
// 控制柄长三分之一段长；两端都是拐角的段直接画直线。
func smoothPath(points []point, cornerCos float64) string {
	n := len(points)
	tangent := make([]point, n)
	corner := make([]bool, n)
	for i, p := range points {
		prev, next := points[(i+n-1)%n], points[(i+1)%n]
		in, out := unit(point{p.x - prev.x, p.y - prev.y}), unit(point{next.x - p.x, next.y - p.y})
		// 转角大于阈值（方向夹角的余弦小于阈值的余弦）就是拐角。
		corner[i] = in.x*out.x+in.y*out.y < cornerCos
		tangent[i] = unit(point{next.x - prev.x, next.y - prev.y})
	}
	var b bytes.Buffer
	b.WriteString("M")
	writePoint(&b, points[0])
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		a, z := points[i], points[j]
		if corner[i] && corner[j] {
			b.WriteString("L")
			writePoint(&b, z)
			continue
		}
		handle := math.Sqrt(sqDist(a, z)) / 3
		c1, c2 := a, z
		if !corner[i] {
			c1 = point{a.x + tangent[i].x*handle, a.y + tangent[i].y*handle}
		}
		if !corner[j] {
			c2 = point{z.x - tangent[j].x*handle, z.y - tangent[j].y*handle}
		}
		b.WriteString("C")
		writePoint(&b, c1)
		b.WriteByte(' ')
		writePoint(&b, c2)
		b.WriteByte(' ')
		writePoint(&b, z)
	}
	b.WriteString("Z")
	return b.String()
}

func unit(p point) point {
	length := math.Hypot(p.x, p.y)
	if length == 0 {
		return point{}
	}
	return point{p.x / length, p.y / length}
}

func writePoint(b *bytes.Buffer, p point) {
	b.WriteString(formatCoord(p.x))
	b.WriteByte(',')
	b.WriteString(formatCoord(p.y))
}

func formatCoord(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}
