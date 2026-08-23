// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const bilibiliAIConclusionAPI = "https://api.bilibili.com/x/web-interface/view/conclusion/get"

var bilibiliWBIMixinKeyOrder = [...]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

type bilibiliWBIResponse struct {
	Code int `json:"code"`
	Data struct {
		WBIImage struct {
			ImageURL string `json:"img_url"`
			SubURL   string `json:"sub_url"`
		} `json:"wbi_img"`
	} `json:"data"`
}

type bilibiliAIConclusionResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Code        int `json:"code"`
		ModelResult struct {
			ResultType int    `json:"result_type"`
			Summary    string `json:"summary"`
		} `json:"model_result"`
	} `json:"data"`
}

func fetchBilibiliAIConclusion(ctx context.Context, raw string, view bilibiliViewResponse) (string, bool) {
	if bilibiliSessdata(ctx) == "" || strings.TrimSpace(view.Data.BVID) == "" {
		return "", false
	}
	cid := selectedBilibiliCID(raw, view)
	if cid == 0 {
		return "", false
	}

	var nav bilibiliWBIResponse
	if !fetchBilibiliJSON(ctx, "https://api.bilibili.com/x/web-interface/nav", "https://www.bilibili.com/", &nav) {
		return "", false
	}
	params := url.Values{
		"bvid": {view.Data.BVID},
		"cid":  {strconv.FormatInt(cid, 10)},
	}
	if view.Data.Owner.Mid > 0 {
		params.Set("up_mid", strconv.FormatInt(view.Data.Owner.Mid, 10))
	}
	signed, ok := signBilibiliWBI(params, nav.Data.WBIImage.ImageURL, nav.Data.WBIImage.SubURL, time.Now())
	if !ok {
		return "", false
	}

	referer := "https://www.bilibili.com/video/" + view.Data.BVID + "/"
	var response bilibiliAIConclusionResponse
	if !fetchBilibiliJSON(ctx, bilibiliAIConclusionAPI+"?"+signed.Encode(), referer, &response) {
		return "", false
	}
	return bilibiliAIConclusionSummary(response)
}

func selectedBilibiliCID(raw string, view bilibiliViewResponse) int64 {
	cid := view.Data.CID
	pageNumber := bilibiliPageFromURL(raw)
	if pageNumber > 0 && pageNumber <= len(view.Data.Pages) && view.Data.Pages[pageNumber-1].CID != 0 {
		cid = view.Data.Pages[pageNumber-1].CID
	}
	return cid
}

func signBilibiliWBI(params url.Values, imageURL, subURL string, now time.Time) (url.Values, bool) {
	imageKey := bilibiliWBIKey(imageURL)
	subKey := bilibiliWBIKey(subURL)
	rawKey := imageKey + subKey
	if len(rawKey) < len(bilibiliWBIMixinKeyOrder) {
		return nil, false
	}
	var mixed strings.Builder
	for _, index := range bilibiliWBIMixinKeyOrder {
		mixed.WriteByte(rawKey[index])
	}
	mixinKey := mixed.String()
	if len(mixinKey) > 32 {
		mixinKey = mixinKey[:32]
	}

	signed := make(url.Values, len(params)+2)
	for key, values := range params {
		for _, value := range values {
			signed.Add(key, filterBilibiliWBIValue(value))
		}
	}
	signed.Set("wts", strconv.FormatInt(now.Unix(), 10))
	canonical := signed.Encode()
	digest := md5.Sum([]byte(canonical + mixinKey))
	signed.Set("w_rid", hex.EncodeToString(digest[:]))
	return signed, true
}

func bilibiliWBIKey(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	base := path.Base(parsed.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

func filterBilibiliWBIValue(value string) string {
	return strings.Map(func(char rune) rune {
		switch char {
		case '!', '\'', '(', ')', '*':
			return -1
		default:
			return char
		}
	}, value)
}

func bilibiliAIConclusionSummary(response bilibiliAIConclusionResponse) (string, bool) {
	if response.Code != 0 || response.Data.Code != 0 || response.Data.ModelResult.ResultType == 0 {
		return "", false
	}
	summary := strings.TrimSpace(response.Data.ModelResult.Summary)
	return summary, summary != ""
}
