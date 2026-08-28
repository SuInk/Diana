// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
)

const (
	imageSourcePluginID = "official.image-source"

	imageSourceSettingSauceNAOEnabled    = "saucenao_enabled"
	imageSourceSettingSauceNAOURL        = "saucenao_url"
	imageSourceSettingSauceNAOAPIKey     = "saucenao_api_key"
	imageSourceSettingMinSimilarity      = "min_similarity"
	imageSourceSettingTraceMoeEnabled    = "tracemoe_enabled"
	imageSourceSettingTraceMoeURL        = "tracemoe_url"
	imageSourceSettingMaxResults         = "max_results"
	imageSourceSettingTimeoutSeconds     = "timeout_seconds"
	imageSourceSettingMaxUploadMegabytes = "max_upload_megabytes"

	defaultSauceNAOURL = "https://saucenao.com/search.php"
	defaultTraceMoeURL = "https://api.trace.moe/search"
)

// ImageSourcePlugin 给模型一个「这张图哪来的」工具。
//
// 两个来源分工不同，不是互为备份：SauceNAO 覆盖插画和同人图（pixiv、danbooru、
// twitter 这些），trace.moe 只认番剧截图但能给到第几集第几分钟。所以两个都跑，
// 各自的结果一起交给模型，让它按问题挑着说。
type ImageSourcePlugin struct {
	client *http.Client
}

func NewImageSourcePlugin(client *http.Client) *ImageSourcePlugin {
	return &ImageSourcePlugin{client: client}
}

func (p *ImageSourcePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          imageSourcePluginID,
		Name:        "图片溯源",
		Version:     "0.1.0",
		Description: "查找聊天里图片的出处：SauceNAO 覆盖插画和同人图并给出 pixiv/danbooru/twitter 原链，trace.moe 认番剧截图并给出集数和时间点。图片会上传到对应服务检索。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:read", "network:http", "llm:tool"},
		Settings: []PluginSettingSpec{
			{
				Key:         imageSourceSettingSauceNAOEnabled,
				Label:       "启用 SauceNAO",
				Description: "插画、同人图和本子的主力来源，需要 API Key。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         imageSourceSettingSauceNAOAPIKey,
				Label:       "SauceNAO API Key",
				Description: "在 saucenao.com 注册后于账号页取得。免费额度是每天 100 次、每 30 秒 4 次；不填则 SauceNAO 不可用。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         imageSourceSettingSauceNAOURL,
				Label:       "SauceNAO 接口地址",
				Description: "仅允许 HTTPS 地址，或本机调试用的 localhost HTTP 地址。",
				Type:        PluginSettingTypeString,
				Default:     defaultSauceNAOURL,
			},
			{
				Key:         imageSourceSettingTraceMoeEnabled,
				Label:       "启用 trace.moe",
				Description: "番剧截图溯源，返回作品、集数和出现时间；不需要密钥。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         imageSourceSettingTraceMoeURL,
				Label:       "trace.moe 接口地址",
				Description: "仅允许 HTTPS 地址，或本机调试用的 localhost HTTP 地址。",
				Type:        PluginSettingTypeString,
				Default:     defaultTraceMoeURL,
			},
			{
				Key:   imageSourceSettingMinSimilarity,
				Label: "相似度下限",
				// 反向图搜的低分结果几乎全是噪声，而模型看到一条「来源」就容易当真。
				Description: "低于这个相似度的结果直接丢掉，不交给模型。搜同人图或二次创作时可以调低，但低于 50 基本都是误报。",
				Type:        PluginSettingTypeNumber,
				Default:     60,
				Min:         settingRange(1),
				Max:         settingRange(100),
				Step:        1,
				Unit:        "%",
			},
			{
				Key:         imageSourceSettingMaxResults,
				Label:       "每个来源结果上限",
				Description: "每个来源最多返回几条候选。",
				Type:        PluginSettingTypeNumber,
				Default:     3,
				Min:         settingRange(1),
				Max:         settingRange(8),
				Step:        1,
				Unit:        "条",
			},
			{
				Key:         imageSourceSettingTimeoutSeconds,
				Label:       "单次查询超时",
				Description: "两个来源是并发跑的，这是各自的等待上限。",
				Type:        PluginSettingTypeNumber,
				Default:     20,
				Min:         settingRange(5),
				Max:         settingRange(60),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         imageSourceSettingMaxUploadMegabytes,
				Label:       "上传大小上限",
				Description: "超过这个大小的图片不上传检索。SauceNAO 自身的上限是 15 MB。",
				Type:        PluginSettingTypeNumber,
				Default:     8,
				Min:         settingRange(1),
				Max:         settingRange(15),
				Step:        1,
				Unit:        "MB",
			},
		},
	}
}

// Handle 不参与消息流：溯源是模型按需调用的动作，不该每见到一张图就自动去查——
// 群里刷图时那是几十次外部请求，免费额度一分钟就烧完了。
func (p *ImageSourcePlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *ImageSourcePlugin) httpClient() *http.Client {
	if p != nil && p.client != nil {
		return p.client
	}
	return http.DefaultClient
}
