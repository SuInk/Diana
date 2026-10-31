// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 行情走腾讯财经的三个公开接口，都不要 Key，A 股、港股、美股一套代码：
//   - smartbox.gtimg.cn 按名称/拼音/代码搜证券，拿到带市场前缀的代码；
//   - qt.gtimg.cn 实时报价，GBK 编码、字段用 ~ 分隔；
//   - web.ifzq.gtimg.cn 日线 K 线（前复权），A 股的键是 qfqday，其他市场是 day。
//
// 字段下标是接口约定，三地在用到的几个位置上一致；成交量单位不同（A 股是手，
// 港美股是股），金额 A 股是万元。
const stockDefaultChartDays = 60

type stockClient struct {
	http      *http.Client
	quoteURL  string
	searchURL string
	klineURL  string
}

func newStockClient() *stockClient {
	return &stockClient{
		http:      &http.Client{Timeout: 10 * time.Second},
		quoteURL:  "https://qt.gtimg.cn/q=",
		searchURL: "https://smartbox.gtimg.cn/s3/?v=2&t=all&c=1&q=",
		klineURL:  "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=",
	}
}

// StockQuote 是一只证券的实时行情。
type StockQuote struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Market     string  `json:"market"`
	Currency   string  `json:"currency"`
	Price      float64 `json:"price"`
	PrevClose  float64 `json:"prev_close"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	Change     float64 `json:"change"`
	ChangePct  float64 `json:"change_pct"`
	Volume     float64 `json:"volume"`
	VolumeUnit string  `json:"volume_unit"`
	Amount     float64 `json:"amount,omitempty"`
	AmountUnit string  `json:"amount_unit,omitempty"`
	Time       string  `json:"time"`
	// klineSymbol 保留报价接口返回的完整证券代码：普通美股带交易所后缀，
	// 指数以点开头。报价请求代码不能直接用于美股日线。
	klineSymbol string
}

// StockBar 是一根日线。
type StockBar struct {
	Date                   string
	Open, Close, High, Low float64
	Volume                 float64
	// Adjustment 只记录接口确认的复权方式；空串表示未确认。
	Adjustment string
}

var (
	stockPrefixedPattern = regexp.MustCompile(`^(?i)(sh|sz|bj|hk|us)([0-9a-z.\-]+)$`)
	stockAShareDigits    = regexp.MustCompile(`^\d{6}$`)
	stockHKDigits        = regexp.MustCompile(`^\d{1,5}$`)
	stockUSTicker        = regexp.MustCompile(`^[A-Za-z][A-Za-z.\-]{0,9}$`)
)

// normalizeStockSymbol 把用户给的代码转成接口要的带前缀代码；认不出来（多半是名称）
// 返回空串，交给搜索。
func normalizeStockSymbol(raw string) string {
	original := strings.TrimSpace(raw)
	value := strings.TrimSuffix(strings.TrimSuffix(strings.ToUpper(original), ".SH"), ".SZ")
	if m := stockPrefixedPattern.FindStringSubmatch(value); m != nil {
		market, code := strings.ToLower(m[1]), m[2]
		switch market {
		case "hk":
			if stockHKDigits.MatchString(code) {
				return "hk" + fmt.Sprintf("%05s", code)
			}
		case "us":
			return "us" + strings.ToUpper(code)
		default:
			if stockAShareDigits.MatchString(code) {
				return market + code
			}
		}
	}
	switch {
	case stockAShareDigits.MatchString(value):
		switch value[0] {
		case '6', '9', '5':
			return "sh" + value
		case '4', '8':
			return "bj" + value
		default:
			return "sz" + value
		}
	case stockHKDigits.MatchString(value):
		return "hk" + fmt.Sprintf("%05s", value)
	case stockUSTicker.MatchString(original) && strings.ToUpper(original) == original:
		// 只认全大写的英文代码；小写或混写更可能是拼音、名称，交给搜索。
		return "us" + value
	}
	return ""
}

// stockIndexAliases 是群里常说的指数简称。搜索接口对「纳指」只返回一串纳指 ETF，
// 对「恒指」「道指」也不稳，这几个直接映射。
var stockIndexAliases = map[string]string{
	"纳指": "usIXIC", "纳斯达克": "usIXIC", "纳斯达克指数": "usIXIC", "纳指100": "usNDX", "纳斯达克100": "usNDX",
	"道指": "usDJI", "道琼斯": "usDJI", "标普": "usINX", "标普500": "usINX",
	"恒指": "hkHSI", "恒生指数": "hkHSI", "恒生科技": "hkHSTECH",
	"上证": "sh000001", "上证指数": "sh000001", "大盘": "sh000001", "深证成指": "sz399001", "深成指": "sz399001",
	"创业板": "sz399006", "创业板指": "sz399006", "沪深300": "sh000300", "科创50": "sh000688",
}

// Resolve 把代码或名称解析成带前缀的代码。
func (c *stockClient) Resolve(ctx context.Context, query string) (string, error) {
	if symbol, ok := stockIndexAliases[strings.ToUpper(strings.TrimSpace(query))]; ok {
		return symbol, nil
	}
	if symbol := normalizeStockSymbol(query); symbol != "" {
		return symbol, nil
	}
	body, err := c.get(ctx, c.searchURL+url.QueryEscape(strings.TrimSpace(query)))
	if err != nil {
		return "", err
	}
	symbol := parseStockSearch(string(body))
	if symbol == "" {
		return "", fmt.Errorf("没找到「%s」对应的股票", strings.TrimSpace(query))
	}
	return symbol, nil
}

// parseStockSearch 优先取搜索结果里第一只股票（GP）或指数（ZS）；没有的话退而取
// 场内基金（ETF/LOF），权证、牛熊证一概不要。
func parseStockSearch(body string) string {
	if symbol := pickStockSearch(body, func(kind string) bool { return strings.HasPrefix(kind, "GP") || kind == "ZS" }); symbol != "" {
		return symbol
	}
	return pickStockSearch(body, func(kind string) bool { return strings.Contains(kind, "ETF") || strings.Contains(kind, "LOF") })
}

func pickStockSearch(body string, accept func(kind string) bool) string {
	start, end := strings.IndexByte(body, '"'), strings.LastIndexByte(body, '"')
	if start < 0 || end <= start {
		return ""
	}
	payload := body[start+1 : end]
	if unquoted, err := strconv.Unquote(`"` + payload + `"`); err == nil {
		payload = unquoted
	}
	for _, item := range strings.Split(payload, "^") {
		fields := strings.Split(item, "~")
		if len(fields) < 5 {
			continue
		}
		if !accept(fields[4]) {
			continue
		}
		market, code := fields[0], fields[1]
		if market == "us" {
			// 美股搜索结果形如 tme.n、tcehy.ps，后缀是交易所，报价接口不要。
			code = stripUSExchangeSuffix(code)
			return "us" + strings.ToUpper(code)
		}
		if market == "sh" || market == "sz" || market == "bj" || market == "hk" {
			return market + code
		}
	}
	return ""
}

// 只移除腾讯的交易所后缀，保留 BRK.B 这样的股份类别和 .IXIC 指数代码。
func stripUSExchangeSuffix(code string) string {
	if dot := strings.LastIndexByte(code, '.'); dot > 0 {
		switch strings.ToUpper(code[dot+1:]) {
		case "OQ", "N", "A", "PS":
			return code[:dot]
		}
	}
	return code
}

// Quote 拉实时报价。
func (c *stockClient) Quote(ctx context.Context, symbol string) (StockQuote, error) {
	body, err := c.get(ctx, c.quoteURL+symbol)
	if err != nil {
		return StockQuote{}, err
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body)
	if err != nil {
		decoded = body
	}
	return parseStockQuote(symbol, string(decoded))
}

func parseStockQuote(symbol, body string) (StockQuote, error) {
	start, end := strings.IndexByte(body, '"'), strings.LastIndexByte(body, '"')
	if start < 0 || end <= start {
		return StockQuote{}, fmt.Errorf("没有 %s 的行情", symbol)
	}
	fields := strings.Split(body[start+1:end], "~")
	if len(fields) < 38 || strings.TrimSpace(fields[1]) == "" {
		return StockQuote{}, fmt.Errorf("没有 %s 的行情", symbol)
	}
	num := func(index int) float64 {
		value, _ := strconv.ParseFloat(strings.TrimSpace(fields[index]), 64)
		return value
	}
	quote := StockQuote{
		Symbol:    symbol,
		Name:      strings.TrimSpace(fields[1]),
		Price:     num(3),
		PrevClose: num(4),
		Open:      num(5),
		Volume:    num(6),
		Change:    num(31),
		ChangePct: num(32),
		High:      num(33),
		Low:       num(34),
		Time:      formatStockTime(fields[30]),

		klineSymbol: symbol,
	}
	if code := strings.TrimSpace(fields[2]); strings.HasPrefix(symbol, "us") && code != "" {
		quote.klineSymbol = "us" + code
	}
	switch symbol[:2] {
	case "hk":
		quote.Market, quote.Currency, quote.VolumeUnit = "港股", "HKD", "股"
		quote.Amount, quote.AmountUnit = num(37), "港元"
	case "us":
		quote.Market, quote.Currency, quote.VolumeUnit = "美股", "USD", "股"
		quote.Amount, quote.AmountUnit = num(37), "美元"
	default:
		quote.Market, quote.Currency, quote.VolumeUnit = "A股", "CNY", "手"
		quote.Amount, quote.AmountUnit = num(37), "万元"
	}
	if quote.Price == 0 && quote.PrevClose == 0 {
		return StockQuote{}, fmt.Errorf("没有 %s 的行情", symbol)
	}
	return quote, nil
}

// formatStockTime 把 A 股的 20261030161455 统一成 2026-10-30 16:14:55，其他原样。
func formatStockTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if parsed, err := time.Parse("20060102150405", raw); err == nil {
		return parsed.Format("2006-01-02 15:04:05")
	}
	return strings.ReplaceAll(raw, "/", "-")
}

// History 请求最近 days 个交易日的前复权日线，按日期升序。
// 是否确实返回前复权数据由响应字段记录到 Adjustment。
func (c *stockClient) History(ctx context.Context, quote StockQuote, days int) ([]StockBar, error) {
	symbol := firstNonEmpty(quote.klineSymbol, quote.Symbol)
	body, err := c.get(ctx, fmt.Sprintf("%s%s,day,,,%d,qfq", c.klineURL, symbol, days))
	if err != nil {
		return nil, err
	}
	return parseStockKline(symbol, body, days)
}

func parseStockKline(symbol string, body []byte, days int) ([]StockBar, error) {
	var payload struct {
		Code int                                   `json:"code"`
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 K 线：%w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("K 线接口返回错误码 %d", payload.Code)
	}
	series := payload.Data[symbol]
	adjustment := "qfq"
	raw, ok := series["qfqday"]
	if !ok {
		raw = series["day"]
		adjustment = ""
	}
	var rows [][]any
	if len(raw) == 0 || json.Unmarshal(raw, &rows) != nil {
		return nil, fmt.Errorf("没有 %s 的 K 线", symbol)
	}
	bars := make([]StockBar, 0, len(rows))
	for _, row := range rows {
		// 每行是 [日期, 开, 收, 高, 低, 量, ...]，数值是字符串。
		if len(row) < 6 {
			return nil, fmt.Errorf("K 线字段不完整")
		}
		date, _ := row[0].(string)
		values := [5]float64{}
		valid := date != ""
		for index := range values {
			text, _ := row[index+1].(string)
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				valid = false
				break
			}
			values[index] = value
		}
		if !valid {
			return nil, fmt.Errorf("K 线日期或价格无法解析")
		}
		bars = append(bars, StockBar{Date: date, Open: values[0], Close: values[1], High: values[2], Low: values[3], Volume: values[4], Adjustment: adjustment})
	}
	// 先校验和去重再截尾，不能靠截断掩盖错误代码返回的陈旧占位数据。
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	unique := bars[:0]
	for _, bar := range bars {
		if len(unique) > 0 && unique[len(unique)-1].Date == bar.Date {
			if unique[len(unique)-1] != bar {
				return nil, fmt.Errorf("K 线同一天存在冲突数据：%s", bar.Date)
			}
			continue
		}
		unique = append(unique, bar)
	}
	bars = unique
	if err := validateStockBars(bars); err != nil {
		return nil, err
	}
	if days > 0 && len(bars) > days {
		bars = bars[len(bars)-days:]
	}
	return bars, nil
}

// 日线允许节假日、停牌和上市不足请求天数，但跨年断层不能当连续近期走势发送。
// 此检查只拒绝可疑图，不插值或猜测缺失价格，实时报价仍可返回。
func validateStockBars(bars []StockBar) error {
	var previous time.Time
	for _, bar := range bars {
		date, err := time.Parse("2006-01-02", bar.Date)
		if err != nil {
			return fmt.Errorf("K 线日期无效：%s", bar.Date)
		}
		for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("K 线数值无效：%s", bar.Date)
			}
		}
		if bar.Low <= 0 || bar.Volume < 0 || bar.Low > math.Min(bar.Open, bar.Close) ||
			bar.High < math.Max(bar.Open, bar.Close) {
			return fmt.Errorf("K 线价格或成交量异常：%s", bar.Date)
		}
		if !previous.IsZero() && (!date.After(previous) || date.Sub(previous) > 366*24*time.Hour) {
			return fmt.Errorf("K 线日期不连续，暂不绘图：%s 至 %s", previous.Format("2006-01-02"), bar.Date)
		}
		previous = date
	}
	return nil
}

func (c *stockClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("请求行情接口：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("行情接口返回 HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 2<<20))
}
