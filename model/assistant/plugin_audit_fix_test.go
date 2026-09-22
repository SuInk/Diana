// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// auditStubPlugin 只声明工具，用来验证工具聚合的行为。
type auditStubPlugin struct {
	manifest PluginManifest
	tools    []agent.Tool
	err      error
}

func (p *auditStubPlugin) Manifest() PluginManifest { return p.manifest }

func (p *auditStubPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *auditStubPlugin) AgentTools(SettingValues) ([]agent.Tool, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.tools, nil
}

type auditStubTool struct{ name string }

func (t *auditStubTool) Name() string        { return t.name }
func (t *auditStubTool) Description() string { return t.name }
func (t *auditStubTool) Run(context.Context, map[string]any) (string, error) {
	return t.name, nil
}

// group_relations 是唯一一个不带 official. 前缀的内置插件，只看前缀挡不住它：
// 第三方清单写成这个 ID 就能换掉插件对象，还继承它已配好的设置。
func TestRegisterPluginRejectsThirdPartyOverBuiltIn(t *testing.T) {
	manager := NewPluginManager(NewGroupRelationsPlugin())
	intruder := &auditStubPlugin{manifest: PluginManifest{
		ID: GroupRelationsPluginID, Name: "冒名插件", Version: "9.9.9",
	}}
	if err := manager.RegisterPlugin(intruder); !errors.Is(err, ErrBuiltInPluginAction) {
		t.Fatalf("err = %v, 想要拒绝顶替内置插件", err)
	}
	state, ok := manager.Get(GroupRelationsPluginID)
	if !ok || !state.Manifest.BuiltIn || state.Manifest.Name == "冒名插件" {
		t.Fatalf("内置插件被改写: %+v", state.Manifest)
	}
}

// 一个插件出问题不该让这一轮所有工具都消失——包括内置的。
func TestAgentToolsSkipsFailingPluginInsteadOfDroppingAll(t *testing.T) {
	healthy := &auditStubPlugin{
		manifest: PluginManifest{ID: "zz.healthy", Name: "正常", Version: "1.0.0"},
		tools:    []agent.Tool{&auditStubTool{name: "healthy_tool"}},
	}
	broken := &auditStubPlugin{
		manifest: PluginManifest{ID: "aa.broken", Name: "出错", Version: "1.0.0"},
		err:      errors.New("配置坏了"),
	}
	manager := NewPluginManager(healthy, broken)
	for _, id := range []string{"zz.healthy", "aa.broken"} {
		if _, err := manager.Install(id); err != nil {
			t.Fatal(err)
		}
	}
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil {
		t.Fatalf("单个插件出错不该让整轮工具失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name() != "healthy_tool" {
		t.Fatalf("tools = %+v", tools)
	}
}

// 重名工具一起发给模型，调哪个是未定义行为；内置排在前面，第三方抢不走名字。
func TestAgentToolsDeduplicatesAndPrefersBuiltIn(t *testing.T) {
	builtin := &auditStubPlugin{
		manifest: PluginManifest{ID: "zz.builtin", Name: "内置", Version: "1.0.0", BuiltIn: true, Official: true},
		tools:    []agent.Tool{&auditStubTool{name: "shared_tool"}},
	}
	// ID 排在内置前面，但它是第三方。
	third := &auditStubPlugin{
		manifest: PluginManifest{ID: "aa.third", Name: "第三方", Version: "1.0.0"},
		tools:    []agent.Tool{&auditStubTool{name: "shared_tool"}, &auditStubTool{name: "own_tool"}},
	}
	manager := NewPluginManager(builtin, third)
	if _, err := manager.Install("aa.third"); err != nil {
		t.Fatal(err)
	}
	if state, ok := manager.Get("zz.builtin"); !ok || !state.Installed || !state.Enabled {
		t.Fatalf("内置插件初始状态异常: %+v", state)
	}
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	if len(names) != 2 || names[0] != "shared_tool" || names[1] != "own_tool" {
		t.Fatalf("重名工具没有按预期去重: %v", names)
	}
	if out, _ := tools[0].Run(context.Background(), nil); out != "shared_tool" {
		t.Fatalf("留下的不是内置工具: %q", out)
	}
}

func TestCompareRepoPluginVersions(t *testing.T) {
	cases := []struct{ next, installed, want string }{
		{"1.0.1", "1.0.0", RepoPluginVersionUpgrade},
		{"1.1.0", "1.0.9", RepoPluginVersionUpgrade},
		{"2.0.0", "10.0.0", RepoPluginVersionDowngrade},
		{"1.0.0", "1.0.0", RepoPluginVersionSame},
		{"0.9.0", "1.0.0", RepoPluginVersionDowngrade},
	}
	for _, testCase := range cases {
		if got := CompareRepoPluginVersions(testCase.next, testCase.installed); got != testCase.want {
			t.Fatalf("Compare(%q,%q)=%q 想要 %q", testCase.next, testCase.installed, got, testCase.want)
		}
	}
}

// ".." 不含路径分隔符，却会让 RemoveAll 删到整个数据目录。
func TestRemoveRepoPluginRejectsDotSegments(t *testing.T) {
	dataDir := t.TempDir()
	marker := filepath.Join(dataDir, "app.db")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"..", ".", "...", ".hidden"} {
		if err := RemoveRepoPlugin(dataDir, id); err == nil {
			t.Fatalf("应拒绝非法 ID %q", id)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("数据目录被删掉了: %v", err)
	}
}
