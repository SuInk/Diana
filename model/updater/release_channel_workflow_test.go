package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestReleaseChannelWorkflow(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name, Run string
				With      map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range workflow.Jobs["build-version"].Steps {
		if step.Name == "Resolve and validate" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing version resolver")
	}
	for _, tc := range []struct{ tag, channel string }{
		{"v1.2.3", "release"}, {"v1.2.3-beta.1", "beta"}, {"v1.2.3-rc.1", "beta"}, {"v1.2.3-alpha.1", ""}, {"v1.2.3-beta.01", ""},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "model/version"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "model/version/VERSION"), []byte(tc.tag), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "output")
			cmd := exec.Command("bash", "-e", "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GITHUB_REF=refs/tags/"+tc.tag, "GITHUB_OUTPUT="+output)
			result, err := cmd.CombinedOutput()
			if tc.channel == "" {
				if err == nil {
					t.Fatal("invalid tag accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, result)
			}
			got, err := os.ReadFile(output)
			if err != nil || !strings.Contains(string(got), "channel="+tc.channel+"\n") {
				t.Fatalf("output %q: %v", got, err)
			}
		})
	}
	for _, step := range workflow.Jobs["docker"].Steps {
		if !strings.HasPrefix(step.Name, "Docker metadata") {
			continue
		}
		for _, line := range strings.Split(step.With["tags"], "\n") {
			if strings.Contains(line, "value=latest") && !strings.Contains(line, "channel == 'release'") {
				t.Errorf("stable tag is not guarded: %s", line)
			}
		}
	}
	for _, step := range workflow.Jobs["release"].Steps {
		if step.Name == "Publish release" && (step.With["prerelease"] != "${{ needs.build-version.outputs.channel != 'release' }}" || step.With["make_latest"] != "${{ needs.build-version.outputs.channel == 'release' }}") {
			t.Fatal("prerelease must not become latest stable release")
		}
	}
}
