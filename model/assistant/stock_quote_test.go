// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestNormalizeStockSymbol(t *testing.T) {
	cases := map[string]string{
		"600519":    "sh600519",
		"000001":    "sz000001",
		"300750":    "sz300750",
		"830799":    "bj830799",
		"600519.SH": "sh600519",
		"sz000001":  "sz000001",
		"SH000001":  "sh000001",
		"00700":     "hk00700",
		"700":       "hk00700",
		"hk700":     "hk00700",
		"AAPL":      "usAAPL",
		"usTSLA":    "usTSLA",
		"BRK.B":     "usBRK.B",
		"贵州茅台":      "",
		"tengxun":   "",
	}
	for input, want := range cases {
		if got := normalizeStockSymbol(input); got != want {
			t.Errorf("normalizeStockSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStockIndexAliasSkipsSearch(t *testing.T) {
	client := newStockClient()
	client.searchURL = "http://127.0.0.1:1/unreachable?q="
	if got, err := client.Resolve(context.Background(), "纳指"); err != nil || got != "usIXIC" {
		t.Fatalf("Resolve(纳指) = %q, %v", got, err)
	}
}

func TestParseStockSearchSkipsWarrantsAndStripsUSSuffix(t *testing.T) {
	body := `v_hint="hk~13005~腾讯购A~txa~QZ^hk~00700~腾讯控股~txkg~GP"`
	if got := parseStockSearch(body); got != "hk00700" {
		t.Fatalf("got %q", got)
	}
	if got := parseStockSearch(`v_hint="us~tme.n~TME~txyl~GP"`); got != "usTME" {
		t.Fatalf("got %q", got)
	}
	if got := parseStockSearch(`v_hint="hk~13005~x~x~QZ^sz~159941~x~x~QDII-ETF"`); got != "sz159941" {
		t.Fatalf("ETF fallback got %q", got)
	}
	if got := parseStockSearch(`v_hint="N"`); got != "" {
		t.Fatalf("got %q", got)
	}
}

func stockQuoteLine(symbol string, fields map[int]string) string {
	values := make([]string, 50)
	for index, value := range fields {
		values[index] = value
	}
	return "v_" + symbol + `="` + strings.Join(values, "~") + `";`
}

func TestParseStockQuoteAShare(t *testing.T) {
	line := stockQuoteLine("sh600519", map[int]string{1: "贵州茅台", 3: "1392.05", 4: "1354.00", 5: "1363.00", 6: "59927",
		30: "20261030161455", 31: "38.05", 32: "2.81", 33: "1393.37", 34: "1352.98", 37: "825738"})
	quote, err := parseStockQuote("sh600519", line)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Name != "贵州茅台" || quote.Price != 1392.05 || quote.ChangePct != 2.81 || quote.Market != "A股" ||
		quote.VolumeUnit != "手" || quote.AmountUnit != "万元" || quote.Time != "2026-10-30 16:14:55" {
		t.Fatalf("unexpected quote %+v", quote)
	}
}

func TestParseStockQuoteUSIndexUsesDottedKlineSymbol(t *testing.T) {
	line := stockQuoteLine("usIXIC", map[int]string{1: "纳斯达克", 2: ".IXIC", 3: "26184.72", 4: "26549.85", 31: "-365.13", 32: "-1.38"})
	quote, err := parseStockQuote("usIXIC", line)
	if err != nil {
		t.Fatal(err)
	}
	if quote.klineSymbol != "us.IXIC" {
		t.Fatalf("klineSymbol = %q", quote.klineSymbol)
	}
}

func TestParseStockQuoteUnknownSymbol(t *testing.T) {
	if _, err := parseStockQuote("sh999999", `v_pv_none_match="1";`); err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestParseStockKlineSortsAndTrims(t *testing.T) {
	body := `{"code":0,"data":{"usAAPL":{"day":[["2011-06-02","1","346.2","1","1","1"],["2026-10-28","1","340.1","1","1","1"],["2026-10-29","1","348.91","1","1","1"]]}}}`
	bars, err := parseStockKline("usAAPL", []byte(body), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[0].Date != "2026-10-28" || bars[1].Close != 348.91 {
		t.Fatalf("unexpected bars %+v", bars)
	}
	aShare := `{"code":0,"data":{"sh600519":{"qfqday":[["2026-10-30","1","1392.05","1","1","1"]]}}}`
	if bars, err := parseStockKline("sh600519", []byte(aShare), 60); err != nil || len(bars) != 1 {
		t.Fatalf("qfqday not read: %v %+v", err, bars)
	}
}

func TestStockClientResolvesNameAndDecodesGBK(t *testing.T) {
	line := stockQuoteLine("hk00700", map[int]string{1: "腾讯控股", 3: "383.600", 4: "377.600", 6: "22269954",
		30: "2026/10/30 16:08:14", 31: "6.000", 32: "1.59", 37: "8540915574"})
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(line)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			if r.URL.Query().Get("q") != "腾讯" {
				t.Errorf("search query = %q", r.URL.Query().Get("q"))
			}
			_, _ = w.Write([]byte(`v_hint="hk~00700~tx~txkg~GP"`))
		case "/quote":
			_, _ = w.Write([]byte(gbk))
		}
	}))
	defer server.Close()
	client := newStockClient()
	client.searchURL = server.URL + "/search?q="
	client.quoteURL = server.URL + "/quote?q="

	symbol, err := client.Resolve(context.Background(), "腾讯")
	if err != nil || symbol != "hk00700" {
		t.Fatalf("Resolve = %q, %v", symbol, err)
	}
	quote, err := client.Quote(context.Background(), symbol)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Name != "腾讯控股" || quote.Currency != "HKD" || quote.Time != "2026-10-30 16:08:14" {
		t.Fatalf("unexpected quote %+v", quote)
	}
}

func TestDianaStockToolWithoutChart(t *testing.T) {
	line := stockQuoteLine("usAAPL", map[int]string{1: "苹果", 3: "348.91", 4: "343.83", 6: "42582126", 30: "2026-10-29 16:00:01", 31: "5.08", 32: "1.48"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(line))
	}))
	defer server.Close()
	tool := newDianaStockTool(nil, MessageEvent{}, SettingValues{stockSettingChart: false}).(*dianaStockTool)
	tool.client.quoteURL = server.URL + "/?q="

	output, err := tool.Run(context.Background(), map[string]any{"query": "AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaStockResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Quote == nil || result.Quote.Symbol != "usAAPL" || result.ChartSent {
		t.Fatalf("unexpected result %s", output)
	}
}

func TestRenderStockChartPNG(t *testing.T) {
	if _, _, err := LoadCJKFont(); err != nil {
		t.Skip("没有中文字体：", err)
	}
	quote := StockQuote{Symbol: "sh600519", Name: "贵州茅台", Market: "A股", Currency: "CNY", Price: 1392.05, Change: 38.05, ChangePct: 2.81}
	bars := []StockBar{{Date: "2026-10-28", Close: 1350}, {Date: "2026-10-29", Close: 1354}, {Date: "2026-10-30", Close: 1392.05}}
	png, err := RenderStockChartPNG(quote, bars)
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 1000 || string(png[1:4]) != "PNG" {
		t.Fatalf("not a PNG (%d bytes)", len(png))
	}
	if _, err := RenderStockChartPNG(quote, bars[:1]); err == nil {
		t.Fatal("expected error with a single bar")
	}
}
