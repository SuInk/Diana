// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 溯源要能按群开关、要有 Key 可填，就必须以插件形式出现在插件页。
func TestImageSourcePluginIsRegistered(t *testing.T) {
	manager := NewDefaultPluginManager()
	var found *PluginManifest
	for _, state := range manager.List() {
		if state.Manifest.ID == imageSourcePluginID {
			manifest := state.Manifest
			found = &manifest
		}
	}
	if found == nil {
		t.Fatal("图片溯源没有注册成内置插件")
	}
	if !found.BuiltIn || !found.Official {
		t.Fatalf("manifest = %#v", *found)
	}
	keys := map[string]bool{}
	secret := map[string]bool{}
	for _, setting := range found.Settings {
		keys[setting.Key] = true
		secret[setting.Key] = setting.Secret
	}
	for _, want := range []string{
		imageSourceSettingSauceNAOEnabled, imageSourceSettingSauceNAOKey,
		imageSourceSettingTraceMoeEnabled, imageSourceSettingMinSimilarity,
		imageSourceSettingMaxResults, imageSourceSettingTimeout, imageSourceSettingPrivateEnabled,
	} {
		if !keys[want] {
			t.Fatalf("缺少设置项 %s：%#v", want, found.Settings)
		}
	}
	// API Key 要按密文存，不能在插件页上明文回显。
	if !secret[imageSourceSettingSauceNAOKey] {
		t.Fatal("SauceNAO API Key 没有标记成密文设置")
	}
	// 上传图片给第三方这件事必须写在说明里，用户才知道自己开了什么。
	if !strings.Contains(found.Description, "上传") {
		t.Fatalf("插件说明没有讲清会把图片上传给第三方：%s", found.Description)
	}
}

// 开了 SauceNAO 但没填 Key 等于没开，得当成不可用——否则用户只会看到「没查到」。
func TestImageSourceProviderAvailability(t *testing.T) {
	withoutKey := imageSourceConfig{SauceNAOEnabled: true}
	if withoutKey.saucenaoUsable() {
		t.Fatal("没有 Key 的 SauceNAO 不该算可用")
	}
	if withoutKey.anyProviderUsable() {
		t.Fatal("两条线都不可用时不该报告可用")
	}
	if reason := imageSourceUnavailableReason(withoutKey); !strings.Contains(reason, "API Key") {
		t.Fatalf("没填 Key 的说明不到位：%s", reason)
	}
	withKey := imageSourceConfig{SauceNAOEnabled: true, SauceNAOKey: "k"}
	if !withKey.saucenaoUsable() || !withKey.anyProviderUsable() {
		t.Fatal("填了 Key 的 SauceNAO 应当可用")
	}
	// trace.moe 不用 Key，单开也能查。
	traceOnly := imageSourceConfig{TraceMoeEnabled: true}
	if !traceOnly.anyProviderUsable() {
		t.Fatal("只开 trace.moe 时应当可用")
	}
}

func TestFilterImageSourceMatches(t *testing.T) {
	cfg := imageSourceConfig{MinSimilarity: 60, MaxResults: 2}
	got := filterImageSourceMatches([]ImageSourceMatch{
		{Provider: "a", Similarity: 55},
		{Provider: "b", Similarity: 91},
		{Provider: "c", Similarity: 60},
		{Provider: "d", Similarity: 77},
	}, cfg)
	if len(got) != 2 {
		t.Fatalf("条数 = %d：%#v", len(got), got)
	}
	if got[0].Provider != "b" || got[1].Provider != "d" {
		t.Fatalf("没有按相似度从高到低截断：%#v", got)
	}
	// 门槛值本身要留下：60 分设成 60 却被过滤掉是最难查的那种「怎么没结果」。
	edge := filterImageSourceMatches([]ImageSourceMatch{{Provider: "c", Similarity: 60}}, imageSourceConfig{MinSimilarity: 60, MaxResults: 3})
	if len(edge) != 1 {
		t.Fatalf("正好等于门槛的结果被丢了：%#v", edge)
	}
}

func TestSearchSauceNAOParsesResults(t *testing.T) {
	var gotFields map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析上传失败：%v", err)
		}
		gotFields = map[string]string{}
		for key, values := range r.MultipartForm.Value {
			gotFields[key] = values[0]
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("没有收到图片：%v", err)
		} else {
			data, _ := io.ReadAll(file)
			if string(data) != "fake-image" {
				t.Errorf("上传内容 = %q", data)
			}
		}
		_, _ = w.Write([]byte(`{"header":{"status":0,"long_remaining":42,"short_remaining":5},"results":[
			{"header":{"similarity":"93.21","index_name":"Pixiv"},"data":{"title":"夏日","member_name":"某位画师","ext_urls":["https://www.pixiv.net/artworks/1"]}},
			{"header":{"similarity":"31.02","index_name":"Danbooru"},"data":{"creator":"another","ext_urls":["https://danbooru.donmai.us/posts/2"]}},
			{"header":{"similarity":"88.00","index_name":"空条目"},"data":{}}
		]}`))
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	cfg := imageSourceConfig{SauceNAOEnabled: true, SauceNAOKey: "test-key", MaxResults: 3}
	matches, err := plugin.searchSauceNAOAt(context.Background(), server.URL, cfg, []byte("fake-image"))
	if err != nil {
		t.Fatal(err)
	}
	// 既没标题也没链接的条目对用户没有意义，不该冒出来。
	if len(matches) != 2 {
		t.Fatalf("结果条数 = %d：%#v", len(matches), matches)
	}
	first := matches[0]
	if first.Similarity < 93 || first.Title != "夏日" || first.Author != "某位画师" || first.Source != "Pixiv" {
		t.Fatalf("第一条解析结果 = %#v", first)
	}
	if len(first.URLs) != 1 || !strings.Contains(first.URLs[0], "pixiv.net") {
		t.Fatalf("链接没解析出来：%#v", first.URLs)
	}
	if gotFields["api_key"] != "test-key" || gotFields["output_type"] != "2" {
		t.Fatalf("请求字段 = %#v", gotFields)
	}
}

// 额度用完和 Key 不对是两种最常见的失败，都要说清楚而不是「没查到」。
func TestSearchSauceNAOReportsQuotaAndRejection(t *testing.T) {
	quota := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"header":{"status":0,"long_remaining":0,"short_remaining":3},"results":[]}`))
	}))
	defer quota.Close()
	plugin := NewImageSourcePlugin(quota.Client())
	cfg := imageSourceConfig{SauceNAOEnabled: true, SauceNAOKey: "k", MaxResults: 3}
	if _, err := plugin.searchSauceNAOAt(context.Background(), quota.URL, cfg, []byte("x")); err == nil || !strings.Contains(err.Error(), "额度") {
		t.Fatalf("额度用完时的报错 = %v", err)
	}

	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"header":{"status":-1,"message":"Invalid API key"},"results":[]}`))
	}))
	defer rejected.Close()
	plugin = NewImageSourcePlugin(rejected.Client())
	if _, err := plugin.searchSauceNAOAt(context.Background(), rejected.URL, cfg, []byte("x")); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("Key 无效时的报错 = %v", err)
	}
}

func TestSearchTraceMoeParsesResults(t *testing.T) {
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		if string(body) != "fake-image" {
			t.Errorf("上传内容 = %q", body)
		}
		_, _ = w.Write([]byte(`{"error":"","result":[
			{"anilist":{"id":21,"title":{"native":"ワンピース","romaji":"One Piece"}},"filename":"op.mkv","episode":1071,"from":83.5,"to":90,"similarity":0.964}
		]}`))
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	matches, err := plugin.searchTraceMoeAt(context.Background(), server.URL, imageSourceConfig{TraceMoeEnabled: true}, []byte("fake-image"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("结果条数 = %d：%#v", len(matches), matches)
	}
	match := matches[0]
	// trace.moe 给的是 0~1 的小数，展示前要换算成百分比，否则 0.96 会被相似度门槛全挡掉。
	if match.Similarity < 96 || match.Similarity > 97 {
		t.Fatalf("相似度没有换算成百分比：%v", match.Similarity)
	}
	if match.Title != "ワンピース" {
		t.Fatalf("番名 = %q", match.Title)
	}
	if !strings.Contains(match.Detail, "1071") || !strings.Contains(match.Detail, "01:23") {
		t.Fatalf("集数和时间点 = %q", match.Detail)
	}
	if len(match.URLs) != 1 || !strings.Contains(match.URLs[0], "anilist.co/anime/21") {
		t.Fatalf("链接 = %#v", match.URLs)
	}
	if !strings.HasPrefix(contentType, "image/") {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

// 不带 anilistInfo 时 anilist 是个纯数字 ID，也得认。
func TestTraceMoeAnilistTitleAcceptsBareID(t *testing.T) {
	title, link := traceMoeAnilistTitle(json.RawMessage(`21`))
	if title != "" || !strings.Contains(link, "anime/21") {
		t.Fatalf("title=%q link=%q", title, link)
	}
	title, link = traceMoeAnilistTitle(json.RawMessage(`{"id":7,"title":{"romaji":"Only Romaji"}}`))
	if title != "Only Romaji" || !strings.Contains(link, "anime/7") {
		t.Fatalf("title=%q link=%q", title, link)
	}
}

// 一条线路挂了不影响另一条：番剧截图只有 trace.moe 认得，插画反过来。
func TestImageSourceSearchKeepsWorkingProvider(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer broken.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"","result":[{"anilist":{"id":21,"title":{"native":"ワンピース"}},"episode":1,"from":1,"similarity":0.97}]}`))
	}))
	defer working.Close()

	plugin := NewImageSourcePlugin(working.Client())
	plugin.saucenaoEndpoint = broken.URL
	plugin.traceMoeEndpoint = working.URL
	cfg := imageSourceConfig{SauceNAOEnabled: true, SauceNAOKey: "k", TraceMoeEnabled: true, MinSimilarity: 60, MaxResults: 3}
	matches, notes := plugin.search(context.Background(), cfg, []byte("x"))
	if len(matches) != 1 || matches[0].Provider != imageSourceProviderTraceMoe {
		t.Fatalf("可用线路的结果没有留下：%#v", matches)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], imageSourceProviderSauceNAO) {
		t.Fatalf("失败线路没有留下说明：%#v", notes)
	}
}

// 没查到不能让模型自由发挥，要给一句明确的结论。
func TestImageSourceSummary(t *testing.T) {
	empty := imageSourceSummary(nil, []string{"saucenao 查询失败：今日额度已用完"})
	if !strings.Contains(empty, "没有查到") || !strings.Contains(empty, "额度") {
		t.Fatalf("空结果结论 = %q", empty)
	}
	found := imageSourceSummary([]ImageSourceMatch{{Provider: "saucenao", Similarity: 62.5}}, nil)
	if !strings.Contains(found, "62.5") || !strings.Contains(found, "80") {
		t.Fatalf("低相似度结论没有提醒这是猜测：%q", found)
	}
}
