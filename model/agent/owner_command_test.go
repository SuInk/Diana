package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOwnerCommandRejectsObviousWholeMachineDestruction(t *testing.T) {
	for _, r := range []OwnerCommand{
		{Command: "/bin/rm", Args: []string{"-rf", "/"}},
		{Command: "/sbin/mkfs.ext4", Args: []string{"/dev/sda"}},
		{Command: "/bin/dd", Args: []string{"if=/dev/zero", "of=/dev/sda"}},
		{Command: "/usr/sbin/diskutil", Args: []string{"eraseDisk", "APFS", "disk0"}},
	} {
		if !obviousDestructiveHostCommand(r) {
			t.Fatalf("accepted destructive command: %+v", r)
		}
	}
	if obviousDestructiveHostCommand(OwnerCommand{Command: "/bin/rm", Args: []string{"-rf", "/tmp/project-cache"}}) {
		t.Fatal("blocked ordinary directory cleanup")
	}
}

func TestOwnerCommandWritesOutsideCWDWithoutInheritingSecrets(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix shell fixture")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "cwd")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "outside-cwd")
	t.Setenv("DIANA_HOST_TEST_SECRET", "must-not-inherit")
	request, err := ValidateOwnerCommand(Config{}, OwnerCommand{Command: "/bin/sh", CWD: cwd, Args: []string{"-c", `printf host > "$1"; printf '%s' "$DIANA_HOST_TEST_SECRET"`, "probe", output}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RunOwnerCommand(context.Background(), Config{}, request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Exit    int    `json:"exit_code"`
		Mode    string `json:"execution_mode"`
		Sandbox bool   `json:"sandbox"`
	}
	if err = json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Exit != 0 || result.Mode != "host_account" || result.Sandbox || strings.Contains(raw, "must-not-inherit") {
		t.Fatal(raw)
	}
	body, err := os.ReadFile(output)
	if err != nil || string(body) != "host" {
		t.Fatalf("host write: %s %v", body, err)
	}
}

func TestOwnerCommandCanAccessHostNetwork(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl unavailable")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("host-network-probe")) }))
	defer server.Close()
	request, err := ValidateOwnerCommand(Config{}, OwnerCommand{Command: curl, CWD: t.TempDir(), Args: []string{"--noproxy", "*", "--max-time", "5", "-fsS", server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RunOwnerCommand(context.Background(), Config{}, request)
	if err != nil || !strings.Contains(raw, "host-network-probe") || !strings.Contains(raw, `"exit_code":0`) {
		t.Fatalf("network: %s %v", raw, err)
	}
}

// 主人命令的输出不在内存里截到 64KiB：装不下单次上限时给尾部整行，完整输出落盘，
// 结果里写明在哪、怎么读。
func TestOwnerCommandPagesLongOutputAndKeepsFullCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	request, err := ValidateOwnerCommand(Config{}, OwnerCommand{Command: "/bin/sh", CWD: t.TempDir(), Args: []string{"-c", "i=0; while [ $i -lt 5000 ]; do echo row-$i; i=$((i+1)); done"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RunOwnerCommand(WithToolOutputBudget(context.Background(), 3000), Config{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(raw) > 3000 {
		t.Fatalf("result %d 字，超过上限", utf8.RuneCountInString(raw))
	}
	var result struct {
		Output         string `json:"output"`
		Notice         string `json:"notice"`
		FullOutputPath string `json:"full_output_path"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Output, "row-") || !strings.HasSuffix(result.Output, "row-4999\n") || !strings.Contains(result.Notice, "共 5000 行") {
		t.Fatalf("tail page wrong: notice=%q output starts %q", result.Notice, result.Output[:20])
	}
	t.Cleanup(func() { _ = os.Remove(result.FullOutputPath) })
	full, err := os.ReadFile(result.FullOutputPath)
	if err != nil || !strings.HasPrefix(string(full), "row-0\n") || strings.Count(string(full), "\n") != 5000 {
		t.Fatalf("full output not kept: %v", err)
	}
	if !strings.Contains(result.Notice, result.FullOutputPath) {
		t.Fatalf("notice does not say where the full output is: %q", result.Notice)
	}
}

func TestOwnerCommandPermissionGuidance(t *testing.T) {
	for _, tc := range []struct{ name, goos, output, kind, status string }{
		{"capture", "darwin", "could not create image from display\n", "screen_recording", "needs_verification"},
		{"sudo", "darwin", "sudo: a password is required\n", "administrator", "manual_action_required"},
		{"noninteractive", "linux", "sudo: a terminal is required to read the password", "administrator", "manual_action_required"},
		{"root", "darwin", "powermetrics must be run as root", "administrator", "manual_action_required"},
		{"generic denial", "darwin", "Permission denied", "", ""},
		{"unsupported sensor", "darwin", "unrecognized sampler: smc", "", ""},
		{"other platform", "linux", "could not create image from display", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ownerCommandPermissionGuidance(tc.goos, tc.output)
			if tc.kind == "" {
				if got != nil {
					t.Fatalf("unexpected guidance: %v", got)
				}
				return
			}
			if got["kind"] != tc.kind || got["status"] != tc.status {
				t.Fatalf("guidance: %v", got)
			}
		})
	}
}

func TestOwnerCommandPermissionGuidanceOnlyOnFailure(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix shell fixture")
	}
	for _, exit := range []string{"0", "1"} {
		request, err := ValidateOwnerCommand(Config{}, OwnerCommand{Command: "/bin/sh", CWD: t.TempDir(), Args: []string{"-c", "printf 'sudo: a password is required\\n'; exit " + exit}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := RunOwnerCommand(context.Background(), Config{}, request)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		guidance, found := result["permission_guidance"].(map[string]any)
		if found != (exit == "1") {
			t.Fatalf("exit %s: %s", exit, raw)
		}
		if found && (guidance["host"] == "" || guidance["executable"] == "") {
			t.Fatalf("missing host context: %s", raw)
		}
	}
}
