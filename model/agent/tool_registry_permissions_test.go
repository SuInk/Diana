package agent

import (
	"context"
	"strings"
	"testing"
)

func TestToolRegistryRetainRemovesUnapprovedTools(t *testing.T) {
	registry := NewToolRegistry(
		&registryPermissionTool{name: "web_search.search"},
		&registryPermissionTool{name: "run_command"},
		&registryPermissionTool{name: "browser_render"},
	)
	registry.SetSkills([]SkillMetadata{{Name: "private-skill"}})
	registry.Retain(map[string]bool{"web_search.search": true, "browser_render": true})
	if registry.Len() != 2 {
		t.Fatalf("tool count = %d", registry.Len())
	}
	if _, ok := registry.Get("run_command"); ok {
		t.Fatal("run_command should have been removed")
	}
	if len(registry.Skills()) != 0 {
		t.Fatalf("skills leaked into restricted registry: %#v", registry.Skills())
	}
}

func TestDefaultToolRegistryDoesNotBypassWebSearchPlugin(t *testing.T) {
	registry, err := NewDefaultToolRegistry(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(WebSearchToolName); ok {
		t.Fatal("web search must be contributed by the installed plugin")
	}
}

func TestToolRegistryCatalogIsCompactAndSchemaFree(t *testing.T) {
	registry := NewToolRegistry(
		&registryPermissionTool{name: "web_search.search", description: `搜索实时网页。input: {"query":"keywords","num_results":10}`},
		&registryPermissionTool{name: "diana.reminder", description: `创建持久提醒。input: {"delay":"1m"}`},
	)

	catalog := registry.Catalog(180)
	if len(catalog) != 2 || catalog[0].Name != "diana.reminder" || catalog[1].Name != "web_search.search" {
		t.Fatalf("catalog = %#v", catalog)
	}
	for _, item := range catalog {
		if item.Description == "" || item.Description == item.Name {
			t.Fatalf("catalog description = %#v", item)
		}
		if containsInputSchema(item.Description) {
			t.Fatalf("catalog leaked input schema: %#v", item)
		}
	}
}

func TestToolRegistryRemoveKeepsRemainingToolsAndSkillMetadata(t *testing.T) {
	registry := NewToolRegistry(
		&registryPermissionTool{name: "diana.image"},
		&registryPermissionTool{name: "skills.list"},
		&registryPermissionTool{name: "skills.read"},
		&registryPermissionTool{name: "web_search.search"},
	)
	registry.SetSkills([]SkillMetadata{{Name: "image-workflow"}})

	registry.Remove("diana.image")
	if _, ok := registry.Get("diana.image"); ok {
		t.Fatal("diana.image should have been removed")
	}
	if got := strings.Join(registry.Names(), ","); got != "skills.list,skills.read,web_search.search" {
		t.Fatalf("tool order = %q", got)
	}
	if len(registry.Skills()) != 1 {
		t.Fatalf("skills = %#v", registry.Skills())
	}

	registry.Remove("skills.list")
	if len(registry.Skills()) != 1 {
		t.Fatalf("skills should remain while skills.read is available: %#v", registry.Skills())
	}
	registry.Remove("skills.read")
	if len(registry.Skills()) != 0 {
		t.Fatalf("skills should be cleared after both skill tools are removed: %#v", registry.Skills())
	}
}

func TestToolRegistryViewKeepsParentToolsLiveAndIsolated(t *testing.T) {
	parent := NewToolRegistry(&registryPermissionTool{name: "extensions.list"})
	parent.SetSkills([]SkillMetadata{{Name: "shared-skill"}})
	view, err := parent.NewView(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	view.Register(&registryPermissionTool{name: "request.only"})
	if _, ok := view.Get("extensions.list"); !ok || len(view.Skills()) != 1 {
		t.Fatalf("view did not inherit parent state: names=%#v skills=%#v", view.Names(), view.Skills())
	}
	parent.Register(&registryPermissionTool{name: "mcp__dynamic__echo"})
	if _, ok := view.Get("mcp__dynamic__echo"); !ok {
		t.Fatal("live parent tool was not visible in existing view")
	}
	view.Remove("mcp__dynamic__echo")
	if _, ok := view.Get("mcp__dynamic__echo"); ok {
		t.Fatal("request view did not hide removed parent tool")
	}
	if _, ok := parent.Get("mcp__dynamic__echo"); !ok {
		t.Fatal("request view mutated parent registry")
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := parent.Get("extensions.list"); !ok {
		t.Fatal("closing request view closed parent registry")
	}
}

func TestToolRegistryDefersParentCloseUntilLastViewCloses(t *testing.T) {
	probe := &registryCloseProbeTool{registryPermissionTool: registryPermissionTool{name: "mcp__shared__probe"}}
	parent := NewToolRegistry(probe)
	view, err := parent.NewView(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if probe.closed != 0 {
		t.Fatal("parent tool closed while a request view was active")
	}
	if _, ok := view.Get(probe.Name()); !ok {
		t.Fatal("active request view lost its parent tool during deferred close")
	}
	if _, err := parent.NewView(Config{WorkDir: t.TempDir()}); err == nil {
		t.Fatal("closing parent accepted a new request view")
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if probe.closed != 1 {
		t.Fatalf("parent tool close count = %d, want 1", probe.closed)
	}
}

func containsInputSchema(description string) bool {
	for _, marker := range []string{"input:", "query\"", "num_results", "delay\""} {
		if strings.Contains(description, marker) {
			return true
		}
	}
	return false
}

type registryPermissionTool struct {
	name        string
	description string
}

type registryCloseProbeTool struct {
	registryPermissionTool
	closed int
}

func (t *registryCloseProbeTool) Close() error {
	t.closed++
	return nil
}

func (t *registryPermissionTool) Name() string { return t.name }
func (t *registryPermissionTool) Description() string {
	if t.description != "" {
		return t.description
	}
	return t.name
}
func (t *registryPermissionTool) Run(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}
