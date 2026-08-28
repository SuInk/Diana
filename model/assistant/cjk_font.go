// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"
)

// 关系图原先只能靠无头浏览器出图，理由写在 RenderGroupRelationHTML 上：位图要自己
// 做字体光栅化，中文名字第一个就过不去。x/image/font 能做这件事，缺的只是一个字型
// 文件——而浏览器画得出中文，本身就说明这台机器上有中文字体，找到它就行。
//
// 这里只找文件，不做渲染：找不到就让调用方退回浏览器，别在没有字形的机器上画出
// 一排豆腐块——那比不出图更糟，因为它看起来像是成功了。
const (
	cjkFontEnvPrimary  = "DIANA_CJK_FONT"
	cjkFontEnvFallback = "DIANA_RELATION_FONT"
)

// cjkFontNameHints 是文件名里出现就大概率含中文字形的片段，按优先级排。
// 先要黑体类的无衬线，关系图上是小字号的人名，衬线和点阵都糊。
var cjkFontNameHints = []string{
	"notosanscjk", "notosanssc", "sourcehansans", "notosansmonocjk",
	"pingfang", "hiraginosansgb", "msyh", "simhei", "microsoftyahei",
	"wqy-microhei", "wqy-zenhei", "droidsansfallback", "arplumingcn",
	"notoserifcjk", "sourcehanserif", "simsun", "unifont",
}

type resolvedCJKFont struct {
	font *sfnt.Font
	path string
	err  error
}

var (
	cjkFontOnce  sync.Once
	cjkFontCache resolvedCJKFont
)

// cjkFontProbeRunes 是挑字体时拿来验货的字。文件名像中文字体不代表真有中文字形：
// 发行版里 unifont 这类补全字体、以及 .ttc 里的点阵分卷，名字都对得上，解析或取字形
// 却会当场失败。挑不出字形就换下一个候选，别把豆腐块画出来——那看起来像成功了。
var cjkFontProbeRunes = []rune{'关', '系', '图'}

// LoadCJKFont 找一个真的能画中文的字体。结果缓存：全盘扫字体是一次不便宜的
// 遍历，而机器上的字体不会在进程生命周期里变来变去。
func LoadCJKFont() (*sfnt.Font, string, error) {
	cjkFontOnce.Do(func() {
		font, path, err := searchUsableCJKFont()
		cjkFontCache = resolvedCJKFont{font: font, path: path, err: err}
	})
	return cjkFontCache.font, cjkFontCache.path, cjkFontCache.err
}

func searchUsableCJKFont() (*sfnt.Font, string, error) {
	for _, name := range []string{cjkFontEnvPrimary, cjkFontEnvFallback} {
		configured := strings.TrimSpace(os.Getenv(name))
		if configured == "" {
			continue
		}
		font, err := loadCJKFont(configured)
		if err != nil {
			return nil, "", fmt.Errorf("%s 指定的字体用不了：%w", name, err)
		}
		if !fontHasCJKGlyphs(font) {
			return nil, "", fmt.Errorf("%s 指定的字体没有中文字形：%s", name, configured)
		}
		return font, configured, nil
	}

	for _, path := range searchCJKFontCandidates() {
		font, err := loadCJKFont(path)
		if err != nil || !fontHasCJKGlyphs(font) {
			continue
		}
		return font, path, nil
	}
	return nil, "", fmt.Errorf("没有找到能画中文的字体文件（可用 %s 指定一个 .ttf/.otf/.ttc）", cjkFontEnvPrimary)
}

// searchCJKFontCandidates 按 hint 的优先级返回候选路径。
func searchCJKFontCandidates() []string {
	found := map[string]string{}
	for _, root := range cjkFontSearchRoots() {
		walkCJKFontRoot(root, found)
	}
	candidates := make([]string, 0, len(found))
	for _, hint := range cjkFontNameHints {
		if path, ok := found[hint]; ok {
			candidates = append(candidates, path)
		}
	}
	return candidates
}

// fontHasCJKGlyphs 验货：字形索引为 0 就是 .notdef，画出来是豆腐块。
func fontHasCJKGlyphs(font *sfnt.Font) bool {
	if font == nil {
		return false
	}
	var buf sfnt.Buffer
	for _, r := range cjkFontProbeRunes {
		index, err := font.GlyphIndex(&buf, r)
		if err != nil || index == 0 {
			return false
		}
	}
	return true
}

func cjkFontSearchRoots() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		roots := []string{"/System/Library/Fonts", "/Library/Fonts"}
		if home != "" {
			roots = append(roots, filepath.Join(home, "Library", "Fonts"))
		}
		return roots
	case "windows":
		root := os.Getenv("WINDIR")
		if root == "" {
			root = `C:\Windows`
		}
		return []string{filepath.Join(root, "Fonts")}
	default:
		roots := []string{"/usr/share/fonts", "/usr/local/share/fonts", "/opt/share/fonts"}
		if home != "" {
			roots = append(roots, filepath.Join(home, ".fonts"), filepath.Join(home, ".local", "share", "fonts"))
		}
		return roots
	}
}

// walkCJKFontRoot 收集一个目录下命中 hint 的字体文件。同一个 hint 只留第一个，
// 遍历失败直接跳过——字体目录不存在、没权限都很正常，不该让整次渲染失败。
func walkCJKFontRoot(root string, out map[string]string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // 单个目录读不动就跳过，不影响其它根目录
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".ttf", ".otf", ".ttc", ".otc":
		default:
			return nil
		}
		name := strings.ToLower(filepath.Base(path))
		for _, hint := range cjkFontNameHints {
			if !strings.Contains(name, hint) {
				continue
			}
			if _, taken := out[hint]; !taken {
				out[hint] = path
			}
		}
		return nil
	})
}

// loadCJKFont 读出字体。.ttc/.otc 是字体集合，opentype.Parse 不认，得走
// ParseCollection 取第一个 —— 文泉驿正黑在很多发行版上就是 .ttc。
func loadCJKFont(path string) (*sfnt.Font, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ttc", ".otc":
		collection, collectionErr := sfnt.ParseCollection(raw)
		if collectionErr != nil {
			return nil, fmt.Errorf("解析字体集合 %s：%w", path, collectionErr)
		}
		// 逐个分卷试，不能只取第 0 个：文泉驿正黑的 .ttc 第一卷是点阵字体，
		// sfnt 直接报 invalid table offset，而后面的矢量卷是好的。
		for index := 0; index < collection.NumFonts(); index++ {
			font, fontErr := collection.Font(index)
			if fontErr != nil {
				continue
			}
			if fontHasCJKGlyphs(font) {
				return font, nil
			}
		}
		return nil, fmt.Errorf("字体集合 %s 里没有能画中文的分卷", path)
	default:
		font, parseErr := sfnt.Parse(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("解析字体 %s：%w", path, parseErr)
		}
		return font, nil
	}
}
