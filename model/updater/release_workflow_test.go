package updater

import (
	"os"
	"path"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestReleaseWorkflowPublishesOnlyCompletePackages(t *testing.T) {
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
	var buildScript, manifestScript string
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
		if matches(uploadPatterns, "dist/"+binary) || matches(publishPatterns, "release/"+binary) {
			t.Errorf("standalone binary %s must not be published", binary)
		}
	}
	for _, fragment := range []string{
		`package_suffix="${package_suffix/darwin-/macos-}"`,
		`package_name="diana-${package_suffix}"`,
		`-o "dist/${package_name}.exe"`,
		`-o "${package_dir}/diana-webui"`,
		`cp -R release/frontend-next-dist/. cmd/webui/webdist/`,
		`"${package_dir}/run.sh"`, `"${package_dir}/uninstall.sh"`,
	} {
		if !strings.Contains(buildScript, fragment) {
			t.Errorf("missing package requirement: %s", fragment)
		}
	}
	if strings.Contains(buildScript, "diana-webui-${{ matrix.suffix }}") {
		t.Error("packages must not ship a second platform-suffixed binary")
	}

	if !strings.Contains(manifestScript, "for file in diana-*.tar.gz diana-windows-*.exe; do") {
		t.Fatal("update rules must cover only the platform downloads")
	}
	// 摘要只写进 latest.json，更新器和安装脚本都从这里取。
	if !strings.Contains(manifestScript, `sha256=$(sha256sum "$file" | cut -d ' ' -f 1)`) {
		t.Fatal("update manifest must carry each package's SHA-256")
	}
}

func TestReleaseAssetNames(t *testing.T) {
	for _, tc := range []struct{ os, arch, name string }{
		{"linux", "amd64", "diana-linux-amd64.tar.gz"},
		{"linux", "arm64", "diana-linux-arm64.tar.gz"},
		{"android", "arm64", "diana-android-arm64.tar.gz"},
		{"darwin", "amd64", "diana-macos-amd64.tar.gz"},
		{"darwin", "arm64", "diana-macos-arm64.tar.gz"},
		{"windows", "amd64", "diana-windows-amd64.exe"},
	} {
		if got := ExpectedReleaseAssetName(tc.os, tc.arch); got != tc.name {
			t.Errorf("got %s, want %s", got, tc.name)
		}
	}
}
