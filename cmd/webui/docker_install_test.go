// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerInstallStartupSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer requires a POSIX shell")
	}
	script, err := filepath.Abs("../../scripts/docker.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, logs, binding string
		want, absent        []string
		fails               bool
	}{
		{
			name:    "first startup with changed published port",
			logs:    "Diana administrator credentials (shown once)\n  username: test-admin\n  password: test-secret\n\nwebui listening on http://[::]:18080\n",
			binding: "0.0.0.0:28080",
			want:    []string{"http://localhost:28080", "宿主机 IP 或域名>:28080", "username: test-admin", "password: test-secret"},
			absent:  []string{"http://localhost:18080"},
		},
		{
			name:    "existing credentials and explicit IPv6 binding",
			logs:    "管理员账号：existing-admin；沿用已有密码\nwebui listening on http://[::]:18080\n",
			binding: "[::1]:38080",
			want:    []string{"http://[::1]:38080", "existing-admin", "重建容器不会重置", "无法显示原密码"},
			absent:  []string{"首次创建的管理员账号与密码"},
		},
		{
			name:   "configured password and no published port",
			logs:   "Diana administrator credentials (shown once)\n  username: configured-admin\n  password: 使用 config.yaml 中的 admin.password\n\nwebui listening on http://[::]:18080\n",
			want:   []string{"admin.password", "未找到容器 18080 端口映射"},
			absent: []string{"http://localhost:18080"},
		},
		{
			name:   "startup timeout does not claim success",
			logs:   "startup failed\n",
			want:   []string{"等待启动超时"},
			absent: []string{"Diana 已启动。"},
			fails:  true,
		},
	} {
		for _, selfUpdate := range []string{"0", "1"} {
			t.Run(tc.name+"/self-update="+selfUpdate, func(t *testing.T) {
				dir := t.TempDir()
				write := func(name, body string) {
					t.Helper()
					path := filepath.Join(dir, name)
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(body), 0700); err != nil {
						t.Fatal(err)
					}
				}
				write("docker-compose.yml", "# existing compose\n")
				write("docker-compose.update.yml", "# existing update helper\n")
				write("scripts/docker/chromium-seccomp.json", "{}")
				write(".env", "DIANA_IMAGE=custom/image\n")
				write("bin/curl", "#!/bin/sh\nexit 99\n")
				write("bin/sleep", "#!/bin/sh\nexit 0\n")
				write("bin/docker", `#!/bin/sh
# Every deployment query must keep the selected Compose file set.
if [ "$1" = compose ] && [ "${2:-}" = -f ]; then
  shift 3
  if [ "$DIANA_DOCKER_SELF_UPDATE" = 1 ]; then
    [ "${1:-}" = -f ] && [ "${2:-}" = docker-compose.update.yml ] || exit 98
    shift 2
  fi
  set -- compose -f docker-compose.yml "$@"
fi
case "$*" in
  'compose -f docker-compose.yml ps -q diana') echo test-container ;;
  'inspect --format {{.State.StartedAt}} test-container') echo 2026-10-11T00:00:00Z ;;
  'compose -f docker-compose.yml logs --no-color --no-log-prefix --since 2026-10-11T00:00:00Z diana')
    if [ ! -f logs-polled ]; then
      touch logs-polled
      echo initializing
    else
      printf '%s' "$TEST_LOGS"
    fi ;;
  'compose -f docker-compose.yml port diana 18080') printf '%s\n' "$TEST_BINDING" ;;
  'compose version' | info | 'compose -f docker-compose.yml config --quiet' | 'compose -f docker-compose.yml pull' | 'compose -f docker-compose.yml up -d') ;;
  *) echo "unexpected docker command: $*" >&2; exit 99 ;;
esac
`)
				cmd := exec.Command("sh", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "DIANA_VARIANT=", "DIANA_DOCKER_SELF_UPDATE="+selfUpdate, "TEST_LOGS="+tc.logs, "TEST_BINDING="+tc.binding)
				output, err := cmd.CombinedOutput()
				if (err != nil) != tc.fails {
					t.Fatalf("error = %v, output:\n%s", err, output)
				}
				for _, want := range tc.want {
					if !strings.Contains(string(output), want) {
						t.Errorf("missing %q in output:\n%s", want, output)
					}
				}
				for _, absent := range tc.absent {
					if strings.Contains(string(output), absent) {
						t.Errorf("unexpected %q in output:\n%s", absent, output)
					}
				}
			})
		}
	}
}
