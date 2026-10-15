package assistant

import (
	"github.com/SuInk/diana/model/llm"
	"testing"
)

func TestDisabledCatalogModelIsSkippedByRoleRouting(t *testing.T) {
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "off-provider", Group: "chat", Config: llm.ProviderConfig{Model: "m", Models: []llm.ModelInfo{{ID: "m", Disabled: true}}}},
		{ID: "on-provider", Group: "chat", Config: llm.ProviderConfig{Model: "m", Models: []llm.ModelInfo{{ID: "m"}}}},
	}}
	if _, err := profilesForModelRole(set, ModelRole{ProfileID: "off-provider", Model: "m"}); err == nil {
		t.Fatal("explicit disabled route accepted")
	}
	routes, err := profilesForModelRole(set, ModelRole{Group: "chat", Model: "m"})
	if err != nil || len(routes) != 1 || routes[0].ID != "on-provider" {
		t.Fatalf("routes=%v error=%v", routes, err)
	}
	if routes := llmProfilesInGroup(set, "chat"); len(routes) != 1 || routes[0].ID != "on-provider" {
		t.Fatal("default routes included disabled model")
	}
}
