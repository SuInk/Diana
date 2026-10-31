package secretmask

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// ChatConfigText is deliberately stricter than Text: configuration addresses are
// private too. Ordinary browsing tools must continue to use Output/Text.
var chatURLLabel = regexp.MustCompile(`(?i)((?:url|endpoint|接口地址|服务地址)\s*[:：=]\s*)[^\s，,;；"<>]+`)

var chatCredentialLabel = regexp.MustCompile(`(?i)((?:密钥|密码|令牌|api[_ -]?key|password|secret|token)\s*[:：=]\s*)[^\s，,;；"<>]+`)

func ChatConfigText(text string) string {
	text = chatCredentialLabel.ReplaceAllString(text, "${1}[REDACTED]")
	text = chatURLLabel.ReplaceAllString(text, "${1}[REDACTED]")
	text = Text(text)
	text = PrivateKeyPattern.ReplaceAllString(text, "[REDACTED]")
	text = TokenPattern.ReplaceAllString(text, "[REDACTED]")
	text = BearerPattern.ReplaceAllString(text, "$1 [REDACTED]")
	text = QuotedCredentialPattern.ReplaceAllString(text, `${1}"[REDACTED]"`)
	return urlPattern.ReplaceAllString(text, "[URL REDACTED]")
}

// ChatConfigValue returns a detached JSON value. Inspect keys before truncating
// text so even short, newly submitted credentials never enter a preview.
func ChatConfigValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return "[REDACTED]"
	}
	var decoded any
	if json.Unmarshal(data, &decoded) != nil {
		return "[REDACTED]"
	}
	return chatConfigValue(decoded)
}

func chatConfigValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		fieldName, hasFieldName := v["key"].(string)
		_, hasLabel := v["label"]
		_, hasRunning := v["running_value"]
		_, hasFile := v["file_value"]
		descriptor := hasFieldName && (hasLabel || hasRunning || hasFile)
		if descriptor && (SensitiveName(fieldName) || strings.Contains(strings.ToLower(fieldName), "url")) {
			for _, name := range []string{"value", "running_value", "file_value"} {
				if item, ok := v[name]; ok && item != nil && item != "" {
					v[name] = "[REDACTED]"
				}
			}
		}
		for key, item := range v {
			lower := strings.ToLower(key)
			sensitive := (SensitiveName(key) && !(key == "key" && descriptor)) || strings.Contains(lower, "url") || strings.HasSuffix(lower, "endpoint") || lower == "headers"
			if sensitive && item != nil {
				if text, ok := item.(string); ok && text == "" {
					continue
				}
				// Counts, switches and flags are useful and aren't credentials.
				switch item.(type) {
				case bool, float64:
					continue
				}
				v[key] = "[REDACTED]"
			} else {
				v[key] = chatConfigValue(item)
			}
		}
		return v
	case []any:
		for i := range v {
			v[i] = chatConfigValue(v[i])
		}
		return v
	case string:
		return ChatConfigText(v)
	default:
		return value
	}
}

func ChatConfigJSON(text string) string {
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return ChatConfigText(text)
	}
	data, err := json.Marshal(ChatConfigValue(value))
	if err != nil {
		return "[REDACTED]"
	}
	return string(data)
}

func ChatConfigError(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{message: ChatConfigText(err.Error()), cause: err}
}

type chatConfigContextKey struct{}

func WithChatConfig(ctx context.Context) context.Context {
	return context.WithValue(ctx, chatConfigContextKey{}, true)
}
func IsChatConfig(ctx context.Context) bool {
	value, _ := ctx.Value(chatConfigContextKey{}).(bool)
	return value
}

// RegisterChatConfigSecrets protects newly submitted credentials before a save
// succeeds, including values later echoed by a validator or generated reply.
func RegisterChatConfigSecrets(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if SensitiveName(key) {
				if text, ok := item.(string); ok {
					Register(text)
				}
			}
			RegisterChatConfigSecrets(item)
		}
	case []any:
		for _, item := range v {
			RegisterChatConfigSecrets(item)
		}
	}
}
