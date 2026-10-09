package agent

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/SuInk/diana/model/desktopctl"
	"github.com/SuInk/diana/model/llm"
)

// Current model, real runner and control plane, deterministic disposable adapter.
// This replay never sends input to the user's desktop.
func TestLiveDesktopReplay(t *testing.T) {
	client := liveAgentClient(t)
	for _, write := range []bool{true, false} {
		name := "readonly"
		if write {
			name = "job_waits_for_human"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			reg := desktopctl.NewRegistry(ctx, nil)
			_, err := reg.SetPolicy(ctx, desktopctl.Policy{Enabled: true, WriteEnabled: write})
			if err != nil {
				t.Fatal(err)
			}
			hub := desktopctl.NewHub(reg)
			defer hub.CloseAll()
			jobs := desktopctl.NewJobManager(ctx, nil)
			hub.SetJobManager(jobs)
			var buf bytes.Buffer
			_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 100, 60)))
			adapter := &desktopctl.MockAdapter{Windows: []desktopctl.WindowInfo{{ID: "fixture", AppName: "Test Editor", BundleID: "com.example.test-editor", Active: true}}, PNG: buf.Bytes()}
			if _, _, err = hub.AttachLocal(ctx, adapter, desktopctl.Hello{}, desktopctl.TokenInfo{}); err != nil {
				t.Fatal(err)
			}
			registry := NewToolRegistry()
			root := t.TempDir()
			registry.RegisterDesktopTools(root, Config{DesktopControl: hub})
			runner, err := NewRunner(client, Config{WorkDir: root, MaxSteps: 12, ToolTimeoutMS: 15000}, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			prompt := "创建一个持久电脑任务，先列窗口找到 Test Editor，截图，然后在该窗口输入 desktop replay。每一步都带 job_id。输入后调用等待主人确认的任务工具，原因写等待人工检查；到这里就停，不要自行确认，不要提交任何东西。"
			if !write {
				prompt = "尝试在 Test Editor 中输入 desktop replay。先列窗口。若工具报告只读就停止，不要重试或宣称已经输入。"
			}
			response, err := runner.Run(ctx, Request{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "你在执行隔离的桌面工具回放测试。只依据工具结果报告进度。"}, {Role: llm.RoleUser, Content: prompt}}})
			if err != nil {
				t.Fatal(err)
			}
			if response == nil {
				t.Fatal("no response")
			}
			if write {
				list := jobs.List("")
				if len(list) != 1 || list[0].Status != desktopctl.JobWaitingConfirm {
					t.Fatalf("expected one waiting task, got %+v", list)
				}
				if len(adapter.Types) != 1 || adapter.Types[0].JobID != list[0].ID {
					t.Fatalf("input not tracked in job: %+v", adapter.Types)
				}
			} else if len(adapter.Types) != 0 {
				t.Fatal("readonly allowed input")
			}
		})
	}
}
