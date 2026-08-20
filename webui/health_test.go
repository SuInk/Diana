// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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

func TestHealthRepositoryDefaultsToOfficialRepo(t *testing.T) {
	handler := NewHealthHandlerWithVersion("v0.8.43")
	if got := handler.Repository(); got != "SuInk/Diana" {
		t.Fatalf("default repository = %q, want SuInk/Diana", got)
	}
}

func TestHealthRepositoryFollowsGitRemote(t *testing.T) {
	handler := NewHealthHandlerWithVersion("v0.8.43")
	handler.SetRepositoryRemote("git@github.com:someone/diana-fork.git")
	if got := handler.Repository(); got != "someone/diana-fork" {
		t.Fatalf("repository = %q, want someone/diana-fork", got)
	}
}

func TestHealthRepositoryKeepsFallbackForNonGitHubRemote(t *testing.T) {
	handler := NewHealthHandlerWithVersion("v0.8.43")
	handler.SetRepositoryRemote("https://gitee.com/someone/diana.git")
	if got := handler.Repository(); got != "SuInk/Diana" {
		t.Fatalf("repository = %q, want fallback SuInk/Diana", got)
	}
}
