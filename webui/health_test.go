package webui

import "testing"

func TestNewHealthHandlerWithVersionPrefersInjectedReleaseVersion(t *testing.T) {
	handler := NewHealthHandlerWithVersion("  v0.8.13  ")
	if handler.version != "v0.8.13" {
		t.Fatalf("health version = %q, want v0.8.13", handler.version)
	}
}

func TestNewHealthHandlerWithVersionFallsBackForDevelopmentBuild(t *testing.T) {
	handler := NewHealthHandlerWithVersion("dev")
	if handler.version == "" {
		t.Fatal("development health version is empty")
	}
}
