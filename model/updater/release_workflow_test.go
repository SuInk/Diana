package updater

import (
	"os"
	"path"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestReleaseWorkflowPublishesCompletePackagesAndStandaloneBinaries(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct{ Goos, Goarch, Suffix string }
				}
			}
			Steps []struct {
				Name, Uses, Run string
				With            map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	build := workflow.Jobs["build"]
	if len(build.Strategy.Matrix.Include) == 0 {
		t.Fatal("missing platform matrix")
	}
	var uploadPatterns, publishPatterns []string
	var buildScript, checksumScript, manifestScript string
	for _, step := range build.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			uploadPatterns = strings.Fields(step.With["path"])
		}
		if step.Name == "Build" {
			buildScript = step.Run
		}
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "softprops/action-gh-release@") {
				publishPatterns = strings.Fields(step.With["files"])
			}
			if step.Name == "Generate SHA-256 checksums" {
				checksumScript = step.Run
			}
			if step.Name == "Generate static update manifest" {
				manifestScript = step.Run
			}
		}
	}
	matches := func(patterns []string, name string) bool {
		for _, pattern := range patterns {
			ok, err := path.Match(pattern, name)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, platform := range build.Strategy.Matrix.Include {
		archive := ExpectedReleaseAssetName(platform.Goos, platform.Goarch)
		binary := "diana-webui-" + platform.Suffix
		if !matches(uploadPatterns, "dist/"+archive) || !matches(publishPatterns, "release/"+archive) {
			t.Errorf("updater archive %s is not published", archive)
		}
		if !matches(uploadPatterns, "dist/"+binary) || !matches(publishPatterns, "release/"+binary) {
			t.Errorf("standalone binary %s is not published", binary)
		}
	}
	for _, name := range []string{"SHA256SUMS", "latest.json"} {
		if !matches(publishPatterns, "release/"+name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, fragment := range []string{
		`package_suffix="${package_suffix/darwin-/macos-}"`,
		`package_name="diana-${package_suffix}"`,
		`-o "${package_dir}/${runtime_name}"`,
		`cp "${package_dir}/${runtime_name}" "${package_dir}/${binary_name}"`,
		`cp -R release/frontend-next-dist/. "${package_dir}/frontend-next/dist/"`,
		`"${package_dir}/run.sh"`, `"${package_dir}/run.bat"`,
	} {
		if !strings.Contains(buildScript, fragment) {
			t.Errorf("missing package requirement: %s", fragment)
		}
	}
	if !strings.Contains(checksumScript, "sha256sum diana-*.tar.gz diana-*.zip diana-webui-* > SHA256SUMS") {
		t.Fatal("checksums must cover complete packages and standalone binaries")
	}
	if !strings.Contains(manifestScript, "for file in diana-*.tar.gz diana-*.zip diana-webui-* SHA256SUMS; do") {
		t.Fatal("update manifest must list packages, standalone binaries and checksums")
	}
}

func TestReleaseAssetNames(t *testing.T) {
	for _, tc := range []struct{ os, arch, name string }{
		{"linux", "amd64", "diana-linux-amd64.tar.gz"},
		{"linux", "arm64", "diana-linux-arm64.tar.gz"},
		{"darwin", "amd64", "diana-macos-amd64.tar.gz"},
		{"darwin", "arm64", "diana-macos-arm64.tar.gz"},
		{"windows", "amd64", "diana-windows-amd64.zip"},
	} {
		if got := ExpectedReleaseAssetName(tc.os, tc.arch); got != tc.name {
			t.Errorf("got %s, want %s", got, tc.name)
		}
		if got := LegacyReleaseAssetName(tc.name); got != strings.Replace(strings.Replace(tc.name, "diana-", "diana-webui-", 1), "macos-", "darwin-", 1) {
			t.Errorf("bad legacy name %s", got)
		}
	}
	for _, name := range []string{"SHA256SUMS", "latest.json", "diana-linux-amd64", "diana-webui-linux-amd64.tar.gz", "../diana-linux-amd64.tar.gz"} {
		if LegacyReleaseAssetName(name) != "" {
			t.Errorf("unexpected alias for %s", name)
		}
	}
}
