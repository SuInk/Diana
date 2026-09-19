package assistant

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveConnection retains the profile's behavior while reading transport settings
// from its source. Sources may be disabled: their connection still serves dependents.
func (s ProfileSet) ResolveConnection(profile BotConfig) (BotConfig, error) {
	id := strings.TrimSpace(profile.ConnectionProfileID)
	if id == "" {
		return profile.WithDefaults(), nil
	}
	source, ok := s.ConfigForProfile(id)
	if !ok {
		return profile, fmt.Errorf("复用连接的来源机器人不存在：%s", id)
	}
	if id == profile.ID {
		return profile, fmt.Errorf("机器人不能复用自己的连接")
	}
	if !IsOneBotPlatform(profile.Platform) || !IsOneBotPlatform(source.Platform) {
		return profile, fmt.Errorf("只能复用 OneBot 机器人的连接")
	}
	if source.ConnectionProfileID != "" {
		return profile, fmt.Errorf("请选择独立连接的来源机器人，不支持链式复用")
	}
	profile.OneBotTransport = source.OneBotTransport
	profile.OneBotWSEndpoint = source.OneBotWSEndpoint
	profile.OneBotHTTPURL = source.OneBotHTTPURL
	profile.OneBotHTTPSecret = source.OneBotHTTPSecret
	profile.OneBotReverseWSEndpoint = source.OneBotReverseWSEndpoint
	profile.OneBotAccessToken = source.OneBotAccessToken
	profile.BotAccount = source.BotAccount
	return profile.WithDefaults(), nil
}

func (s ProfileSet) ValidateConnections() error {
	// Reverse WS and HTTP each have a single process-wide listener. Refuse an
	// explicit alias that would silently lose to a different enabled root.
	listenerOwners := map[string]string{}
	for _, profile := range s.Profiles {
		if !profile.Enabled {
			continue
		}
		resolved, err := s.ResolveConnection(profile)
		if err != nil {
			return err
		}
		if !IsOneBotPlatform(resolved.Platform) || resolved.OneBotTransport == OneBotTransportForwardWS {
			continue
		}
		root := profile.ID
		if profile.ConnectionProfileID != "" {
			root = profile.ConnectionProfileID
		}
		if _, ok := listenerOwners[resolved.OneBotTransport]; !ok {
			listenerOwners[resolved.OneBotTransport] = root
		}
	}
	for _, profile := range s.Profiles {
		if profile.ConnectionProfileID == "" {
			continue
		}
		resolved, err := s.ResolveConnection(profile)
		if err != nil {
			return err
		}
		if profile.Enabled && resolved.OneBotTransport != OneBotTransportForwardWS && listenerOwners[resolved.OneBotTransport] != profile.ConnectionProfileID {
			return fmt.Errorf("复用来源与当前监听连接冲突，请选择当前连接的来源机器人，或先停用其他独立连接")
		}
		if resolved.Enabled && resolved.OneBotTransport == OneBotTransportReverseWS && strings.TrimSpace(resolved.OneBotAccessToken) == "" {
			return fmt.Errorf("机器人「%s」的复用来源未配置反向 WebSocket Access Token，请先配置来源连接", profile.Name)
		}
		resolved.ConnectionProfileID = ""
		if err := resolved.Validate(); err != nil {
			return fmt.Errorf("机器人「%s」的复用连接不可用：%w", profile.Name, err)
		}
	}
	return nil
}

// websocketConnectionKey compares addresses, not credentials. Explicit aliases
// and other transports are excluded. Fragments are never sent in a WS handshake.
func websocketConnectionKey(profile BotConfig) string {
	if profile.ConnectionProfileID != "" || !IsOneBotPlatform(profile.Platform) {
		return ""
	}
	mode := profile.OneBotTransport
	if mode == "" {
		mode = OneBotTransportReverseWS
	}
	endpoint := profile.OneBotReverseWSEndpoint
	if mode == OneBotTransportForwardWS {
		endpoint = profile.OneBotWSEndpoint
	} else if mode != OneBotTransportReverseWS {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if port != "" && !(parsed.Scheme == "ws" && port == "80") && !(parsed.Scheme == "wss" && port == "443") {
		host += ":" + port
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	query := ""
	if parsed.RawQuery != "" {
		query = "?" + parsed.RawQuery
	}
	return mode + "|" + parsed.Scheme + "://" + host + path + query
}

// ValidateIndependentConnection protects individual saves/enables without
// preventing unrelated edits or disabling conflicting legacy profiles.
func (s ProfileSet) ValidateIndependentConnection(profile BotConfig) error {
	key := websocketConnectionKey(profile)
	if key == "" {
		return nil
	}
	for _, source := range s.Profiles {
		if source.ID == profile.ID && profile.ID != "" {
			continue
		}
		if websocketConnectionKey(source) == key {
			return fmt.Errorf("该 WebSocket 地址已由机器人「%s」使用，请在连接来源中选择复用它的连接，或填写不同的地址", source.Name)
		}
	}
	return nil
}
