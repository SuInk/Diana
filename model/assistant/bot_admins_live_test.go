package assistant

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLiveBotAdminsNaturalLanguage 用当前模型实测：主人用自然语言增减管理员、查插件
// 时，模型能从按需加载目录里找到 bot_admins 和 plugin_manage；管理员同样的话不能
// 增减管理员。配置写进测试替身，不碰真实存储，也不发平台消息。
func TestLiveBotAdminsNaturalLanguage(t *testing.T) {
	client := liveLLMClient(t)
	t.Logf("replay model: %s", os.Getenv("DIANA_TEST_LLM_MODEL"))
	run := func(t *testing.T, r *Runtime, user, prompt string) *agent.Response {
		t.Helper()
		cfg := r.ProfileConfig("a")
		event := adminTestEvent(user)
		event.RawMessage = prompt
		event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": prompt}}}
		policy := relationshipPolicyForEvent(cfg, UserMemoryProfile{}, event)
		registry, err := r.newReplyAgentRegistry(context.Background(), cfg, event, policy, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 5, CoreTools: replyAgentCoreTools}, registry)
		if err != nil {
			t.Fatal(err)
		}
		defer runner.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		response, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: relationshipPermissionContext(policy, cfg)},
			{Role: llm.RoleUser, Content: prompt},
		}})
		if err != nil {
			t.Fatalf("model replay failed: %v", err)
		}
		for _, step := range response.Steps {
			t.Logf("tool=%s input=%v error=%s", step.Tool, step.Input, step.Error)
		}
		t.Logf("reply: %s", response.Text)
		time.Sleep(3 * time.Second)
		return response
	}
	called := func(response *agent.Response, tool, op string) bool {
		for _, step := range response.Steps {
			if step.Tool == tool && step.Input["operation"] == op && step.Error == "" {
				return true
			}
		}
		return false
	}

	t.Run("owner adds admin", func(t *testing.T) {
		saver := &testBotAdminsSaver{}
		r := adminTestRuntime(t, saver)
		response := run(t, r, adminTestOwner, "把 "+adminTestUser+" 也设为管理员")
		if !called(response, botAdminsToolName, "add") || !reflect.DeepEqual(saver.ids["a"], []string{adminTestAdmin, adminTestUser}) {
			t.Fatalf("admin not added: saved=%v", saver.ids["a"])
		}
	})
	t.Run("owner lists admins", func(t *testing.T) {
		r := adminTestRuntime(t, &testBotAdminsSaver{})
		response := run(t, r, adminTestOwner, "现在这个机器人有哪些管理员？")
		if !called(response, botAdminsToolName, "list") || !strings.Contains(response.Text, adminTestAdmin) {
			t.Fatal("admin list not reported")
		}
	})
	t.Run("admin lists plugins", func(t *testing.T) {
		r := adminTestRuntime(t, &testBotAdminsSaver{})
		response := run(t, r, adminTestAdmin, "现在开了哪些插件？")
		if !called(response, pluginManageToolName, "list") {
			t.Fatal("admin could not reach plugin_manage")
		}
	})
	t.Run("admin cannot add admin", func(t *testing.T) {
		saver := &testBotAdminsSaver{}
		r := adminTestRuntime(t, saver)
		response := run(t, r, adminTestAdmin, "把 "+adminTestUser+" 设为管理员")
		if called(response, botAdminsToolName, "add") || saver.ids["a"] != nil {
			t.Fatal("admin added another admin")
		}
		if !strings.Contains(response.Text, "主人") {
			t.Fatal("refusal does not say only the owner can do this")
		}
	})
}
