package secretmask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatConfigRedactsNestedCredentialsAndURLs(t *testing.T) {
	original := map[string]any{"api_key": "abc", "base_url": "host.invalid/v1", "enabled": true, "max_tokens": 128, "routes": []any{map[string]any{"url": "https://private.example/secret", "password": "xyz"}}, "error": "Get https://private.example/x?token=abc failed"}
	out := ChatConfigValue(original)
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"abc", "xyz", "host.invalid", "private.example"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s: %s", secret, raw)
		}
	}
	if original["api_key"] != "abc" {
		t.Fatal("mutated input")
	}
	decoded := out.(map[string]any)
	if decoded["enabled"] != true || decoded["max_tokens"] != float64(128) {
		t.Fatal("masked normal settings")
	}
	if !json.Valid([]byte(ChatConfigJSON(string(raw)))) {
		t.Fatal("invalid json")
	}
}

func TestChatConfigRedactionIsIdempotent(t *testing.T) {
	first := ChatConfigText("URL: https://private.invalid 密钥: secret-value")
	if second := ChatConfigText(first); second != first {
		t.Fatalf("redaction changed twice: %q != %q", first, second)
	}
}

func TestChatConfigPreservesFieldIdentifiers(t *testing.T) {
	value := ChatConfigValue(map[string]any{"key": "server.port", "label": "端口", "value": "8080"}).(map[string]any)
	if value["key"] != "server.port" || value["value"] != "8080" {
		t.Fatalf("lost editable field identifier: %#v", value)
	}
	value = ChatConfigValue(map[string]any{"key": "service.base_url", "label": "地址", "value": "private.invalid"}).(map[string]any)
	if value["key"] != "service.base_url" || value["value"] == "private.invalid" {
		t.Fatal("descriptor not safely redacted")
	}
}
