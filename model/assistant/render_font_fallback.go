package assistant

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/netguard"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/net/html"
)

//go:embed render_assets/NotoSans-OFL.txt
var notoSansLicense []byte

//go:embed render_assets/NotoEmoji-OFL.txt
var notoEmojiLicense []byte

type renderFontAsset struct {
	name, sha string
	script    *unicode.RangeTable
	sample    string
	hints     []string
}

var renderFontAssets = []renderFontAsset{
	{"NotoSans", "b85c38ecea8a7cfb39c24e395a4007474fa5a4fc864f6ee33309eb4948d232d5", nil, "AΩЖ", []string{"notosans", "arial", "dejavusans", "liberationsans", "segoe"}},
	{"NotoSansArabic", "ceea25b464a656dc3b26849bab9356740401af62aedf1bfa8b7f0d9b75925b1b", unicode.Arabic, "مرحبا", []string{"notosansarabic", "geeza", "arial", "tahoma", "segoe"}},
	{"NotoSansHebrew", "a7fa16fffb27bedb060a0866267c29e9859aeb9c21cc33f5b3aaf6eb062eca85", unicode.Hebrew, "שלום", []string{"notosanshebrew", "arial", "tahoma", "segoe"}},
	{"NotoSansThai", "404ddfb5ed0aaa6b6ec8a85700d682978992062d67da93903967b56cbd9a4acc", unicode.Thai, "ไทย", []string{"notosansthai", "thonburi", "leelawadee", "tahoma"}},
	{"NotoSansDevanagari", "385e78e6359a9d88a0f243d53b1209d7548361ba2194e2b9ec779bcaa7e8949d", unicode.Devanagari, "नमस्ते", []string{"notosansdevanagari", "devanagari", "kohinoor", "nirmala"}},
	{"NotoSansBengali", "6300c5370cd688b0641343de4c786de6d412bb6c578d129dae75e93a0322dcab", unicode.Bengali, "বাংলা", []string{"notosansbengali", "bangla", "bengali", "nirmala"}},
	{"NotoSansTamil", "6532db33b8b264abe3a098a40619feb489b5ddf5ab1d2b46e72b51eeb548001b", unicode.Tamil, "தமிழ்", []string{"notosanstamil", "tamil", "nirmala"}},
	{"NotoSansSymbols2", "630846d528dbe4c4981370a4d0a9475a1fd1491a129bb411f8e157cdb5de13c6", nil, "→", []string{"notosanssymbols", "symbol", "seguisym"}},
	{"NotoEmoji", "de6c18832938afc99caf132b39d6a30a19bac7f2e812e28db2535b4608d27551", nil, "😀", []string{"notoemoji", "notocoloremoji", "applecoloremoji", "seguiemj"}},
}
var renderFontMu sync.Mutex
var renderFontCache = map[string]string{}

func fontCoversGlyphs(font *sfnt.Font, chars []rune) bool {
	if font == nil {
		return false
	}
	var buffer sfnt.Buffer
	for _, r := range chars {
		idx, err := font.GlyphIndex(&buffer, r)
		if err != nil || idx == 0 {
			return false
		}
	}
	return true
}
func loadFontForGlyphs(path string, chars []rune) (*sfnt.Font, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if collection, err := sfnt.ParseCollection(raw); err == nil {
		for i := 0; i < collection.NumFonts(); i++ {
			font, err := collection.Font(i)
			if err == nil && fontCoversGlyphs(font, chars) {
				return font, nil
			}
		}
	} else if font, err := sfnt.Parse(raw); err == nil && fontCoversGlyphs(font, chars) {
		return font, nil
	}
	return nil, fmt.Errorf("字体 %s 缺少所需字形或不支持解析", filepath.Base(path))
}
func visibleRenderText(page string) string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style") {
			return
		}
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
			out.WriteRune(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out.String()
}
func renderFontNeeds(text string) (map[string][]rune, []rune) {
	groups := map[string][]rune{}
	var unknown []rune
	seen := map[rune]bool{}
	for _, r := range text {
		if seen[r] || unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0xfe0e || r == 0xfe0f {
			continue
		}
		seen[r] = true
		key := ""
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef):
			key = "cjk"
		case (r >= 0x1f000 && r <= 0x1faff) || (r >= 0x2600 && r <= 0x27bf):
			key = "NotoEmoji"
		case unicode.In(r, unicode.Latin, unicode.Greek, unicode.Cyrillic) || r < 0x0250:
			key = "NotoSans"
		default:
			for _, asset := range renderFontAssets {
				if asset.script != nil && unicode.Is(asset.script, r) {
					key = asset.name
					break
				}
			}
		}
		if key == "" && (unicode.IsPunct(r) || unicode.IsNumber(r)) {
			key = "NotoSans"
		}
		if key == "" && unicode.IsSymbol(r) {
			key = "NotoSansSymbols2"
		}
		if key != "" {
			groups[key] = append(groups[key], r)
		} else if unicode.IsLetter(r) || unicode.IsMark(r) {
			unknown = append(unknown, r)
		}
	}
	return groups, unknown
}
func findSystemRenderFont(hints []string, chars []rune) string {
	var paths []string
	for _, root := range cjkFontSearchRoots() {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".ttf" && ext != ".otf" && ext != ".ttc" && ext != ".otc" {
				return nil
			}
			name := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(filepath.Base(path)))
			if strings.Contains(name, "lastresort") {
				return nil
			}
			for _, hint := range hints {
				if strings.Contains(name, hint) {
					paths = append(paths, path)
					break
				}
			}
			return nil
		})
	}
	for _, path := range paths {
		if _, err := loadFontForGlyphs(path, chars); err == nil {
			return path
		}
	}
	return ""
}
func ensureRenderScriptFont(ctx context.Context, asset renderFontAsset, chars []rune) (string, bool, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", false, err
	}
	destination := filepath.Join(root, "diana", "fonts", asset.name+"-"+asset.sha[:8], asset.name+".ttf")
	key := destination
	renderFontMu.Lock()
	cached := renderFontCache[key]
	renderFontMu.Unlock()
	if cached != "" {
		if _, err := loadFontForGlyphs(cached, chars); err == nil {
			return cached, cached == destination, nil
		}
	}
	if _, err := loadFontForGlyphs(destination, chars); err == nil {
		return destination, true, nil
	}
	if path := findSystemRenderFont(asset.hints, chars); path != "" {
		renderFontMu.Lock()
		renderFontCache[key] = path
		renderFontMu.Unlock()
		return path, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cjkFontInstallTimeout)
	defer cancel()
	select {
	case cjkFontInstallGate <- struct{}{}:
		defer func() { <-cjkFontInstallGate }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
	if _, err := loadFontForGlyphs(destination, chars); err == nil {
		return destination, true, nil
	}
	source := "https://raw.githubusercontent.com/notofonts/noto-fonts/ffebf8c1ee449e544955a7e813c54f9b73848eac/hinted/ttf/" + asset.name + "/" + asset.name + "-Regular.ttf"
	license := notoSansLicense
	if asset.name == "NotoEmoji" {
		source = "https://raw.githubusercontent.com/google/fonts/1edf95b4328bc5997ca93d2c0c7205272ec7347f/ofl/notoemoji/NotoEmoji%5Bwght%5D.ttf"
		license = notoEmojiLicense
	}
	if err := downloadVerifiedFont(ctx, netguard.NewPublicHTTPClient(2*time.Minute), source, destination, asset.sha, chars, license); err != nil {
		return "", false, fmt.Errorf("补齐 %s 字体失败：%w", asset.name, err)
	}
	return destination, true, nil
}

// Download only fonts required by visible text. System faces remain the first
// choice; downloaded faces are staged beside the generated HTML for Chromium.
func prepareRenderFontHTML(ctx context.Context, page string) (string, []string, error) {
	groups, unknown := renderFontNeeds(visibleRenderText(page))
	var files []string
	covered := []*sfnt.Font{}
	if chars := groups["cjk"]; len(chars) > 0 {
		font, path, err := ensureCJKFont(ctx)
		if err != nil {
			return "", nil, err
		}
		if !fontCoversGlyphs(font, chars) {
			return "", nil, fmt.Errorf("当前中日韩字体缺少部分字形，请配置覆盖这些字符的字体")
		}
		covered = append(covered, font)
		managed, _ := managedCJKFontPath()
		if path == managed || configuredCJKFont() {
			files = append(files, path)
		}
	}
	var families []string
	for _, asset := range renderFontAssets {
		chars := groups[asset.name]
		if len(chars) == 0 {
			continue
		}
		already := false
		for _, font := range covered {
			if fontCoversGlyphs(font, chars) {
				already = true
				break
			}
		}
		if already {
			continue
		}
		path, embed, err := ensureRenderScriptFont(ctx, asset, chars)
		if err != nil {
			return "", nil, err
		}
		font, err := loadFontForGlyphs(path, chars)
		if err != nil {
			return "", nil, err
		}
		covered = append(covered, font)
		if embed {
			files = append(files, path)
		} else {
			var b sfnt.Buffer
			if family, err := font.Name(&b, sfnt.NameIDFamily); err == nil {
				families = append(families, fmt.Sprintf("%q", family))
			}
		}
	}
	if len(unknown) > 0 {
		missing := []rune{}
		for _, r := range unknown {
			found := false
			for _, font := range covered {
				if fontCoversGlyphs(font, []rune{r}) {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, r)
			}
		}
		for len(missing) > 0 {
			path := findSystemRenderFont([]string{""}, []rune{missing[0]})
			if path == "" {
				return "", nil, fmt.Errorf("尚未覆盖文字 U+%04X 的字体，请提供含对应字形的字体", missing[0])
			}
			font, err := loadFontForGlyphs(path, []rune{missing[0]})
			if err != nil {
				return "", nil, err
			}
			var b sfnt.Buffer
			if family, err := font.Name(&b, sfnt.NameIDFamily); err == nil {
				families = append(families, fmt.Sprintf("%q", family))
			}
			var rest []rune
			for _, r := range missing {
				if !fontCoversGlyphs(font, []rune{r}) {
					rest = append(rest, r)
				}
			}
			missing = rest
		}
	}
	if len(files) == 0 {
		return page, nil, nil
	}

	var css strings.Builder
	css.WriteString("<style>")
	for i := range files {
		family := fmt.Sprintf("DianaFont%d", i)
		fmt.Fprintf(&css, `@font-face{font-family:"%s";src:url("diana-font-%d.ttf")} `, family, i)
		families = append(families, fmt.Sprintf("%q", family))
	}
	stack := strings.Join(families, ",") + "," + renderFontStack
	fmt.Fprintf(&css, `body,.render-root,svg text,svg tspan,svg foreignObject *{font-family:%s!important}code,pre,kbd,samp{font-family:"SFMono-Regular",Menlo,Consolas,%s,monospace!important}</style>`, stack, strings.Join(families, ","))
	insertion := strings.Index(strings.ToLower(page), "</head>")
	if insertion < 0 {
		return css.String() + page, files, nil
	}
	return page[:insertion] + css.String() + page[insertion:], files, nil
}

// The Go raster path uses one font and no complex shaping. Route these labels
// to Chromium instead of producing disconnected letters or missing glyphs.
func relationTextNeedsBrowser(font *sfnt.Font, text string) bool {
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		if unicode.In(r, unicode.Arabic, unicode.Hebrew, unicode.Thai, unicode.Devanagari, unicode.Bengali, unicode.Tamil) || unicode.IsMark(r) || unicode.Is(unicode.Cf, r) || (r >= 0x1f000 && r <= 0x1faff) {
			return true
		}
		if !fontCoversGlyphs(font, []rune{r}) {
			return true
		}
	}
	return false
}
