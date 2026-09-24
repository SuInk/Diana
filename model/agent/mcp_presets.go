// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MCPPreset 是一条内置的 MCP 接入模板：告诉界面要问用户哪几个字段，以及怎么
// 把这些字段拼成一份 MCP 配置。
//
// 预设只是填表的模板，装上之后就是一条普通的 MCP 服务，改配置、停用、删除都走
// 原来那套。少数预设会随 Diana 一起打包上游的二进制（目前只有 gitea-mcp），这
// 等于把别人的发布链路接进我们的供应链，所以只收官方发布的产物，版本和 SHA-256
// 钉在 scripts/fetch-gitea-mcp.sh 里，对不上就构建失败。
type MCPPreset struct {
	ID string `json:"id"`
	// Name 是装上以后这条 MCP 的名字，用户可以改。
	Name       string               `json:"name"`
	Title      string               `json:"title"`
	Summary    string               `json:"summary"`
	DocsURL    string               `json:"docs_url,omitempty"`
	Transports []MCPPresetTransport `json:"transports"`
}

// MCPPresetTransport 是同一个服务的一种接法。远程和本地进程要问的东西不一样，
// 所以字段跟着接法走，而不是堆在一起让用户自己猜哪些该填。
type MCPPresetTransport struct {
	ID     string           `json:"id"`
	Label  string           `json:"label"`
	Hint   string           `json:"hint,omitempty"`
	Fields []MCPPresetField `json:"fields"`
	// Verifiable 告诉界面这一种接法能不能在保存前验凭据，好决定要不要给「检测」
	// 按钮。它由 MCPPresetList 按 verify 是否存在填，不手写，免得和实现对不上。
	Verifiable bool `json:"verifiable,omitempty"`
	config     func(map[string]string) map[string]any
	// values 是 config 的反向：从已保存的配置里取回各字段。非机密字段回填进编辑表，
	// 机密字段只拿去算掩码、或者在主人点「显示」时给 reveal 用，读配置时不回显；
	// 在界面上留空表示「保持原值」。
	values func(mcpServerConfig) map[string]string
	// verify 拿拼好的配置去问一次服务端，确认这套凭据当真能用。收的是最终配置而
	// 不是表单值：编辑时令牌可以留空表示沿用旧的，只有配置里才有那个旧值。没有
	// verify 的接法就是没法在保存前验证（例如令牌根本不经 Diana 的手）。
	verify func(context.Context, mcpServerConfig) (string, error)
}

// ErrPresetCredentialRejected 表示服务端明确拒绝了这套凭据——令牌错了或过期了。
// 和「连不上」分开：前者不该保存，后者只是这台机器现在够不着，值得放行。
var ErrPresetCredentialRejected = errors.New("凭据被拒绝")

type MCPPresetField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Hint        string `json:"hint,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Secret 的值读配置时只给掩码，和 MCP 的请求头、环境变量一样；原文只有主人在
	// WebUI 里点「显示明文」走 reveal 才拿得到。
	Secret bool `json:"secret,omitempty"`
}

// mcpPresets 是内置清单。加一条服务只要往这里加，界面按字段自己渲染。
var mcpPresets = []MCPPreset{giteaMCPPreset(), mcdonaldsMCPPreset(), luckinMCPPreset()}

// giteaMCPPreset 接 Gitea 官方的 gitea-mcp：它同时支持 stdio 和 HTTP，实例地址
// 和访问令牌走 GITEA_HOST / GITEA_ACCESS_TOKEN，自建实例填自己的域名即可。
//
// 二进制随 Diana 一起发布，镜像和 Release 包里都有，所以默认那一项是「填地址和
// 令牌就能用」；已经自己跑了一份 gitea-mcp 的，仍然可以走 HTTP 接上去。
func giteaMCPPreset() MCPPreset {
	return MCPPreset{
		ID:      "gitea",
		Name:    "gitea",
		Title:   "Gitea",
		Summary: "接入自建或公有 Gitea 的仓库、Issue 与 Pull Request。官方 gitea-mcp 随 Diana 一起打包，填实例地址和访问令牌就能用。",
		DocsURL: "https://gitea.com/gitea/gitea-mcp",
		Transports: []MCPPresetTransport{
			{
				ID:    "stdio",
				Label: "用自带的 gitea-mcp",
				Hint:  "推荐：Diana 直接拉起随包发布的 gitea-mcp，令牌只存在这条 MCP 的环境变量里，不经过第三方。",
				Fields: []MCPPresetField{
					{Key: "host", Label: "Gitea 实例地址", Placeholder: "https://git.example.com", Required: true},
					{Key: "token", Label: "访问令牌", Hint: "Gitea 里生成的个人访问令牌，按 MCP 环境变量存放，保存后只显示掩码。", Required: true, Secret: true},
					{Key: "command", Label: "可执行文件", Placeholder: "留空用自带的那份", Hint: "只有要换成自己编译或另外安装的 gitea-mcp 时才填，可填命令名或绝对路径。"},
				},
				config: func(values map[string]string) map[string]any {
					command := strings.TrimSpace(values["command"])
					if command == "" {
						command = bundledGiteaMCPCommand()
					}
					return map[string]any{
						"command": command,
						"args":    []any{"-t", "stdio"},
						"env": map[string]any{
							"GITEA_HOST":         values["host"],
							"GITEA_ACCESS_TOKEN": values["token"],
						},
					}
				},
				values: func(cfg mcpServerConfig) map[string]string {
					values := map[string]string{"host": cfg.Env["GITEA_HOST"], "token": cfg.Env["GITEA_ACCESS_TOKEN"]}
					// 自带的那份是留空的意思，回填成绝对路径会让人以为自己填过。
					// 裸名字同样算自带：没带这份二进制的旧版本就是这么存下来的。
					if command := strings.TrimSpace(cfg.Command); command != "" &&
						command != bundledGiteaMCPCommand() && command != giteaMCPBinaryName() {
						values["command"] = command
					}
					return values
				},
				verify: verifyGiteaToken,
			},
			{
				ID:    "http",
				Label: "连接已经跑起来的 gitea-mcp",
				Hint:  "已经用官方 Docker 镜像或别的机器跑了一份 gitea-mcp（-t http）时填它的地址。令牌配在那一侧，Diana 不经手。",
				Fields: []MCPPresetField{
					{Key: "url", Label: "gitea-mcp 服务地址", Placeholder: "http://127.0.0.1:8080/mcp", Required: true},
					{Key: "authorization", Label: "Authorization 请求头", Placeholder: "留空表示不带", Hint: "只有给 gitea-mcp 另加了鉴权时才需要填。", Secret: true},
				},
				config: func(values map[string]string) map[string]any {
					cfg := map[string]any{"url": values["url"]}
					if token := strings.TrimSpace(values["authorization"]); token != "" {
						cfg["headers"] = map[string]any{"Authorization": token}
					}
					return cfg
				},
				values: func(cfg mcpServerConfig) map[string]string {
					return map[string]string{"url": cfg.URL, "authorization": cfg.Headers["Authorization"]}
				},
				// 这条接法的 Gitea 令牌配在对面那份 gitea-mcp 上，Diana 手里没有，
				// 验不了；这里的 Authorization 是不是对，要连上去才知道，交给「测试连接」。
			},
		},
	}
}

// bearerTokenPreset 是「官方托管一个远程 MCP，拿一个 Bearer 令牌接进去」这一类
// 服务的共同形状：地址是固定的，用户只需要贴令牌。麦当劳和瑞幸都是这样。
type bearerTokenPreset struct {
	ID       string
	Name     string
	Title    string
	Summary  string
	DocsURL  string
	Endpoint string
	Label    string
	Hint     string
	// TokenHint 写清这个令牌在哪申请、能干什么。能下单付款的必须在这里说明白。
	TokenHint string
}

func (spec bearerTokenPreset) preset() MCPPreset {
	return MCPPreset{
		ID:      spec.ID,
		Name:    spec.Name,
		Title:   spec.Title,
		Summary: spec.Summary,
		DocsURL: spec.DocsURL,
		Transports: []MCPPresetTransport{
			{
				ID:    "http",
				Label: spec.Label,
				Hint:  spec.Hint,
				Fields: []MCPPresetField{
					{Key: "token", Label: "访问令牌", Hint: spec.TokenHint, Required: true, Secret: true},
					{Key: "url", Label: "服务地址", Placeholder: spec.Endpoint, Hint: "官方地址已经填好，除非官方改了地址，否则不用动。"},
				},
				config: func(values map[string]string) map[string]any {
					endpoint := strings.TrimSpace(values["url"])
					if endpoint == "" {
						endpoint = spec.Endpoint
					}
					cfg := map[string]any{"url": endpoint}
					// 令牌留空是「沿用已保存的那个」，这时候一个字段都不能写：
					// 写个 "Bearer " 进去，保存那段会把它当成新值，把旧令牌顶掉。
					if token := strings.TrimSpace(values["token"]); token != "" {
						cfg["headers"] = map[string]any{"Authorization": bearerCredential(token)}
					}
					return cfg
				},
				values: func(cfg mcpServerConfig) map[string]string {
					values := map[string]string{"token": cfg.Headers["Authorization"]}
					if url := strings.TrimSpace(cfg.URL); url != "" && url != spec.Endpoint {
						values["url"] = url
					}
					return values
				},
				verify: verifyRemoteMCPCredential,
			},
		},
	}
}

// bearerCredential 允许直接粘贴带 Bearer 前缀的整行，不重复加一遍。
func bearerCredential(token string) string {
	token = strings.TrimSpace(token)
	if rest := strings.TrimSpace(strings.TrimPrefix(token, "Bearer")); len(rest) < len(token) && rest != "" {
		return "Bearer " + rest
	}
	return "Bearer " + token
}

// mcdonaldsMCPPreset 接麦当劳中国官方托管的 MCP：远程 Streamable HTTP，令牌在
// 官方控制台用手机号登录后激活。
func mcdonaldsMCPPreset() MCPPreset {
	return bearerTokenPreset{
		ID:       "mcdonalds",
		Name:     "mcdonalds",
		Title:    "麦当劳中国",
		Summary:  "麦当劳中国官方 MCP：查门店、菜单与营养信息，领麦麦省优惠券、积分兑换，以及麦乐送、到店取餐、得来速、团餐点单。令牌等同于点单权限，能直接下单付款。",
		DocsURL:  "https://github.com/M-China/mcd-mcp-server",
		Endpoint: "https://mcp.mcd.cn",
		Label:    "官方远程服务",
		Hint:     "麦当劳中国托管，不用自己跑任何东西，贴上令牌就能用。仅面向中国大陆（不含港澳台），每个令牌每分钟最多 600 次请求。",
		TokenHint: "在 open.mcd.cn/mcp 用手机号登录后于控制台激活。这个令牌等同于你的点单权限，能直接下单付款，" +
			"所以这条服务默认只有主人能用——放开给群成员等于让别人用你的账号点餐。",
	}.preset()
}

// luckinMCPPreset 接瑞幸官方托管的 MCP，形状和麦当劳那条一样。
func luckinMCPPreset() MCPPreset {
	return bearerTokenPreset{
		ID:       "luckin",
		Name:     "luckin",
		Title:    "瑞幸咖啡",
		Summary:  "瑞幸官方 MCP：查附近门店和商品、预览价格、一句话点单与再来一单。令牌等同于点单权限，能直接下单付款。",
		DocsURL:  "https://open.lkcoffee.com",
		Endpoint: "https://gwmcp.lkcoffee.com/order/user/mcp",
		Label:    "官方远程服务",
		Hint:     "瑞幸托管，不用自己跑任何东西，贴上令牌就能用。",
		TokenHint: "用日常点单的手机号登录 open.lkcoffee.com 自助获取。这个令牌等同于你的点单权限，能直接下单付款，" +
			"所以这条服务默认只有主人能用——放开给群成员等于让别人用你的账号点单。",
	}.preset()
}

// verifyRemoteMCPCredential 对远程 MCP 只做一次握手：连上、协商、拿到服务端信息就
// 断开，不调用任何业务工具（这类服务的工具是会真的下单的，拿来验令牌显然不行）。
//
// 令牌不对时远程网关在 HTTP 这一层就打回来，所以按状态码文本判断是不是凭据问题。
// 认错了也不至于出事：判成凭据问题只是拒绝保存，判不出来就退化成一条警告。
func verifyRemoteMCPCredential(ctx context.Context, cfg mcpServerConfig) (string, error) {
	if strings.TrimSpace(cfg.Headers["Authorization"]) == "" {
		return "", errors.New("请先填写访问令牌")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := connectMCPSession(ctx, "preset", cfg, "", 20*time.Second)
	if err != nil {
		if remoteMCPRejectedCredential(err) {
			return "", fmt.Errorf("%w：服务端不认这个令牌，请确认没填错、没过期", ErrPresetCredentialRejected)
		}
		return "", err
	}
	defer func() { _ = session.session.Close() }()
	// 握手只能证明令牌被接受，换不出「这是谁的账号」，所以不硬编一个名字回去。
	return "", nil
}

func remoteMCPRejectedCredential(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"401", "403", "unauthorized", "forbidden"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// verifyGiteaToken 拿填好的地址和令牌问一次 Gitea 的 /api/v1/user：能换回用户名
// 才算这套凭据可用。不这么问的话，令牌错了在这里什么都看不出来——gitea-mcp 的
// 握手和工具发现根本不碰令牌，「测试连接」照样是绿的，真正的 401 要等到某次对话
// 里调用工具才冒出来。
//
// 这一个请求故意不走 netguard 的公网客户端：自建 Gitea 常常就在内网，按公网规则
// 校验会把它们全挡掉，而这个地址是主人自己填的，填完 gitea-mcp 本来也要连它。
// 代价是多了一个由管理员指定目标的出站请求，所以收窄到只此一种：固定 GET 这一条
// 路径、不跟重定向、不回显响应内容，只取用户名。
func verifyGiteaToken(ctx context.Context, cfg mcpServerConfig) (string, error) {
	host := strings.TrimRight(strings.TrimSpace(cfg.Env["GITEA_HOST"]), "/")
	token := strings.TrimSpace(cfg.Env["GITEA_ACCESS_TOKEN"])
	if host == "" || token == "" {
		return "", errors.New("请先填写实例地址和访问令牌")
	}
	parsed, err := url.Parse(host)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("实例地址要是完整的 http(s) 地址，例如 https://git.example.com")
	}
	if parsed.User != nil {
		return "", errors.New("实例地址里不要带用户名和密码")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.JoinPath("/api/v1/user").String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("Accept", "application/json")
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// 跟着跳就可能把令牌送去另一台主机，这里宁可报错让人把地址填对。
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("连不上 %s：%w", parsed.Host, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("%w：Gitea 不认这个访问令牌，请确认没填错、没过期，并且有读取用户信息的权限", ErrPresetCredentialRejected)
	case response.StatusCode != http.StatusOK:
		return "", fmt.Errorf("Gitea 返回 %s，请确认地址指向 Gitea 实例本身", response.Status)
	}
	var account struct {
		Login string `json:"login"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取 Gitea 响应失败：%w", err)
	}
	if err := json.Unmarshal(body, &account); err != nil || strings.TrimSpace(account.Login) == "" {
		return "", errors.New("这个地址没有返回 Gitea 的用户信息，请确认它指向 Gitea 实例本身")
	}
	return account.Login, nil
}

// presetByID 找到一条内置预设。
func presetByID(id string) (MCPPreset, bool) {
	for _, preset := range mcpPresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return MCPPreset{}, false
}

// presetTransportByID 找到某个预设的某种接法。
func presetTransportByID(presetID, transportID string) (MCPPresetTransport, bool) {
	for _, preset := range mcpPresets {
		if preset.ID != presetID {
			continue
		}
		for _, transport := range preset.Transports {
			if transport.ID == transportID {
				return transport, true
			}
		}
	}
	return MCPPresetTransport{}, false
}

// presetValuesFromConfig 把已保存的配置还原成预设表单里的非机密字段。
func presetValuesFromConfig(cfg mcpServerConfig) map[string]string {
	return presetFieldValues(cfg, false)
}

// presetSecretValuesFromConfig 取回预设表单里的机密字段原文，只给算掩码和 reveal 用。
func presetSecretValuesFromConfig(cfg mcpServerConfig) map[string]string {
	return presetFieldValues(cfg, true)
}

func presetFieldValues(cfg mcpServerConfig, secret bool) map[string]string {
	transport, ok := presetTransportByID(cfg.Preset, cfg.PresetTransport)
	if !ok || transport.values == nil {
		return map[string]string{}
	}
	values := transport.values(cfg)
	for _, field := range transport.Fields {
		if field.Secret != secret {
			delete(values, field.Key)
		}
	}
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			delete(values, key)
		}
	}
	return values
}

// presetVerifyConfig 验一遍这份配置。预设或接法没有 verify 时返回 false，调用方
// 据此告诉用户「这条没法提前验」，而不是假装验过了。
func presetVerifyConfig(ctx context.Context, cfg mcpServerConfig) (string, bool, error) {
	transport, ok := presetTransportByID(cfg.Preset, cfg.PresetTransport)
	if !ok || transport.verify == nil {
		return "", false, nil
	}
	account, err := transport.verify(ctx, cfg)
	return account, true, err
}

// bundledGiteaMCPCommand 指向随 Diana 一起发布的那份 gitea-mcp。它就放在主程序
// 旁边，不在 PATH 里，所以必须给绝对路径。找不到就退回裸名字交给 PATH：宿主机
// 自己装过的照样能用，真的两头都没有时报的是「找不到命令」，比在这里提前失败好查。
func bundledGiteaMCPCommand() string {
	name := giteaMCPBinaryName()
	executable, err := os.Executable()
	if err != nil {
		return name
	}
	// 一键安装会在 PATH 里放软链，顺着链接找才能落到真正的安装目录。
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return bundledCommandIn(filepath.Dir(executable), name)
}

// giteaMCPBinaryName 是这份二进制在各平台上的文件名。
func giteaMCPBinaryName() string {
	if runtime.GOOS == "windows" {
		return "gitea-mcp.exe"
	}
	return "gitea-mcp"
}

// bundledCommandIn 在指定目录里找这个命令，没有就退回裸名字交给 PATH。
func bundledCommandIn(dir, name string) string {
	if path, ok := bundledCommandPath(dir, name); ok {
		return path
	}
	return name
}

// bundledCommandPath 判断这个目录里到底有没有这份二进制。
func bundledCommandPath(dir, name string) (string, bool) {
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, true
	}
	return "", false
}

// MCPPresetList 返回内置清单，供界面渲染。
func MCPPresetList() []MCPPreset {
	out := make([]MCPPreset, 0, len(mcpPresets))
	for _, preset := range mcpPresets {
		transports := make([]MCPPresetTransport, 0, len(preset.Transports))
		for _, transport := range preset.Transports {
			transport.Verifiable = transport.verify != nil
			transports = append(transports, transport)
		}
		preset.Transports = transports
		out = append(out, preset)
	}
	return out
}

// mcpPresetConfig 按预设和接法拼出一份 MCP 配置。必填项缺一个就直接说是哪一个，
// 不要等连接失败再让人回头猜。
//
// storedSecrets 为真时，机密字段可以留空：编辑一条已经装好的服务时，令牌留空的
// 意思是「沿用已保存的那个」，保存那段会把旧值填回来。
func mcpPresetConfig(presetID, transportID string, values map[string]string, storedSecrets bool) (map[string]any, error) {
	for _, preset := range mcpPresets {
		if preset.ID != presetID {
			continue
		}
		for _, transport := range preset.Transports {
			if transport.ID != transportID {
				continue
			}
			clean := map[string]string{}
			for _, field := range transport.Fields {
				value := strings.TrimSpace(values[field.Key])
				if field.Required && value == "" && !(field.Secret && storedSecrets) {
					return nil, fmt.Errorf("请填写「%s」", field.Label)
				}
				clean[field.Key] = value
			}
			return transport.config(clean), nil
		}
		return nil, fmt.Errorf("预设 %s 没有 %s 这种接法", presetID, transportID)
	}
	return nil, fmt.Errorf("预设不存在")
}
