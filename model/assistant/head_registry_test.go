package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 检索到的记忆只在模型用不了 memory 工具时才自动带上。
func TestRetrievedMemoryOnlyWithoutMemoryTool(t *testing.T) {
	withTool := agent.NewToolRegistry(&dianaMemoryTool{})
	if !memoryToolAvailable(true, withTool) {
		t.Fatal("agent with the memory tool must look memories up itself")
	}
	if memoryToolAvailable(false, withTool) || memoryToolAvailable(true, agent.NewToolRegistry()) || memoryToolAvailable(true, nil) {
		t.Fatal("without the agent or the memory tool, retrieved memories must still be injected")
	}
}

// 渲染用的注册表按主人本人来建：发送者换成主人，其余不动。
func TestOwnerRenderEventUsesOwnerID(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "20002"}
	got := ownerRenderEvent(BotConfig{OwnerID: "10001"}, event)
	if got.UserID != "10001" || got.GroupID != "20001" {
		t.Fatalf("render event = %+v", got)
	}
	if ownerRenderEvent(BotConfig{}, event).UserID != "20002" {
		t.Fatal("without an owner the event stays as is")
	}
	if ownerToolScope(RelationshipPolicy{Admin: true}).Admin {
		t.Fatal("the render scope is the owner, not an admin")
	}
}
