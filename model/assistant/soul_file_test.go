// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timeForSoulTest() time.Time { return time.Unix(1_788_247_000, 0) }

type soulTestSaver struct {
	saved map[string]string
}

func (s *soulTestSaver) SaveBotConfig(cfg BotConfig) {
	if s.saved == nil {
		s.saved = map[string]string{}
	}
	s.saved[strings.TrimSpace(cfg.ID)] = strings.TrimSpace(cfg.SystemPrompt)
}

func soulTestRuntime(t *testing.T, saver ConfigSaver, profiles ...BotConfig) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	runtime.SetProfiles(ProfileSet{Profiles: profiles})
	return runtime
}

func soulProfile(id, name, persona string) BotConfig {
	return BotConfig{ID: id, Name: name, Platform: PlatformOneBotV11, BotAccount: "42", SystemPrompt: persona}
}

func readSoul(t *testing.T, dir, profileID string) soulFile {
	t.Helper()
	raw, err := os.ReadFile(SoulFilePath(dir, profileID))
	if err != nil {
		t.Fatal(err)
	}
	return parseSoulFile(string(raw))
}

func writeRawSoul(t *testing.T, dir, profileID, content string) {
	t.Helper()
	if err := os.WriteFile(SoulFilePath(dir, profileID), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 每台机器人一份，文件名就是机器人 ID：改显示名不该让同步认不出这是同一台。
func TestSyncSoulFilesExportsOnePerBot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "souls")
	runtime := soulTestRuntime(t, &soulTestSaver{},
		soulProfile("bot-a", "主号", "你叫甲，说话简短。"),
		soulProfile("bot-b", "小号", "你叫乙，爱开玩笑。"))

	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for _, result := range results {
		if result.Action != SoulSyncCreated {
			t.Fatalf("action = %#v", result)
		}
	}
	if got := readSoul(t, dir, "bot-a").Body; got != "你叫甲，说话简短。" {
		t.Fatalf("bot-a body = %q", got)
	}
	if got := readSoul(t, dir, "bot-b").Body; got != "你叫乙，爱开玩笑。" {
		t.Fatalf("bot-b body = %q", got)
	}
	// 第二轮什么都不该做：同步不能每次都重写文件，否则文件时间戳一直在变，
	// 人也分不清自己有没有改过。
	results, err = runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Action != SoulSyncUnchanged {
			t.Fatalf("second pass action = %#v", result)
		}
	}
	// 目录留空表示没启用：一个文件都不碰，连目录都不建。
	disabledDir := filepath.Join(t.TempDir(), "unused")
	if results, err := runtime.SyncSoulFiles(""); err != nil || results != nil {
		t.Fatalf("disabled sync = %#v, err=%v", results, err)
	}
	if _, err := os.Stat(disabledDir); !os.IsNotExist(err) {
		t.Fatalf("disabled sync touched the disk: %v", err)
	}
}

// 手工放进来的纯 Markdown 文件是最主要的上手路径：首次接管以文件为准。
func TestSyncSoulFilesAdoptsHandWrittenFile(t *testing.T) {
	dir := t.TempDir()
	saver := &soulTestSaver{}
	runtime := soulTestRuntime(t, saver, soulProfile("bot-a", "主号", "库里的旧人设"))
	writeRawSoul(t, dir, "bot-a", "# 甲\n\n你叫甲，说话简短。\n")

	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != SoulSyncImported {
		t.Fatalf("results = %#v", results)
	}
	if got := runtime.ProfileConfig("bot-a").SystemPrompt; got != "# 甲\n\n你叫甲，说话简短。" {
		t.Fatalf("persona = %q", got)
	}
	if got := saver.saved["bot-a"]; got != "# 甲\n\n你叫甲，说话简短。" {
		t.Fatalf("persisted = %q", got)
	}
	// 导入之后写回元信息，下一轮才分辨得出是谁改的。
	if readSoul(t, dir, "bot-a").SyncedHash == "" {
		t.Fatal("synced hash was not written back")
	}
}

func TestSyncSoulFilesImportsFileEditsAndExportsWebUIEdits(t *testing.T) {
	dir := t.TempDir()
	saver := &soulTestSaver{}
	runtime := soulTestRuntime(t, saver, soulProfile("bot-a", "主号", "第一版人设"))
	if _, err := runtime.SyncSoulFiles(dir); err != nil {
		t.Fatal(err)
	}

	// 人改文件：元信息里的哈希还停在上一轮，据此判定文件被动过。
	existing := readSoul(t, dir, "bot-a")
	writeRawSoul(t, dir, "bot-a", "---\nprofile_id: bot-a\nsynced_hash: "+existing.SyncedHash+"\n---\n\n文件里改的人设\n")
	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != SoulSyncImported {
		t.Fatalf("file edit action = %#v", results[0])
	}
	if got := runtime.ProfileConfig("bot-a").SystemPrompt; got != "文件里改的人设" {
		t.Fatalf("persona after file edit = %q", got)
	}

	// 在 WebUI 里改（这里直接改运行时配置模拟）：这次该把文件写回去。
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{soulProfile("bot-a", "主号", "WebUI 改的人设")}})
	results, err = runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != SoulSyncExported {
		t.Fatalf("db edit action = %#v", results[0])
	}
	if got := readSoul(t, dir, "bot-a").Body; got != "WebUI 改的人设" {
		t.Fatalf("exported body = %q", got)
	}
}

// 两边都改了不猜：数据库是运行时真相源，保住它，文件那份另存一旁。直接覆盖会
// 悄悄吃掉一份手写的人设。
func TestSyncSoulFilesKeepsBothSidesOnConflict(t *testing.T) {
	dir := t.TempDir()
	runtime := soulTestRuntime(t, &soulTestSaver{}, soulProfile("bot-a", "主号", "第一版人设"))
	if _, err := runtime.SyncSoulFiles(dir); err != nil {
		t.Fatal(err)
	}
	// 文件改成一份，数据库改成另一份，元信息里的哈希两边都对不上。
	writeRawSoul(t, dir, "bot-a", "---\nprofile_id: bot-a\nsynced_hash: sha256:stale\n---\n\n文件那一版\n")
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{soulProfile("bot-a", "主号", "数据库那一版")}})

	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != SoulSyncConflict || results[0].ConflictPath == "" {
		t.Fatalf("conflict result = %#v", results[0])
	}
	if got := runtime.ProfileConfig("bot-a").SystemPrompt; got != "数据库那一版" {
		t.Fatalf("persona after conflict = %q", got)
	}
	if got := readSoul(t, dir, "bot-a").Body; got != "数据库那一版" {
		t.Fatalf("file after conflict = %q", got)
	}
	saved, err := os.ReadFile(results[0].ConflictPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "文件那一版") {
		t.Fatalf("conflict copy = %q", saved)
	}
}

// 正文为空不当成「把人设清空」：更可能是编辑器写坏了或文件被截断。
func TestSyncSoulFilesRefusesEmptyBody(t *testing.T) {
	dir := t.TempDir()
	runtime := soulTestRuntime(t, &soulTestSaver{}, soulProfile("bot-a", "主号", "第一版人设"))
	if _, err := runtime.SyncSoulFiles(dir); err != nil {
		t.Fatal(err)
	}
	writeRawSoul(t, dir, "bot-a", "---\nprofile_id: bot-a\nsynced_hash: sha256:stale\n---\n\n\n")

	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.ProfileConfig("bot-a").SystemPrompt; got != "第一版人设" {
		t.Fatalf("persona = %q", got)
	}
	if results[0].Action != SoulSyncExported || !strings.Contains(results[0].Message, "为空") {
		t.Fatalf("result = %#v", results[0])
	}
	// 也不该为一份空文件留冲突副本，那只是噪音。
	if results[0].ConflictPath != "" {
		t.Fatalf("conflict copy for an empty file: %q", results[0].ConflictPath)
	}
	if got := readSoul(t, dir, "bot-a").Body; got != "第一版人设" {
		t.Fatalf("file after empty body = %q", got)
	}
}

// 我们写进文件的说明是给人看的，不能被当成人设正文导进数据库——那会每轮都发给模型。
func TestParseSoulFileDropsOurOwnComments(t *testing.T) {
	rendered := renderSoulFile(soulProfile("bot-a", "主号", ""), "你叫甲。", "sha256:abc", timeForSoulTest())
	parsed := parseSoulFile(rendered)
	if parsed.Body != "你叫甲。" {
		t.Fatalf("body = %q", parsed.Body)
	}
	if parsed.SyncedHash != "sha256:abc" {
		t.Fatalf("hash = %q", parsed.SyncedHash)
	}
	if !strings.Contains(rendered, "bot: 主号") || !strings.Contains(rendered, "profile_id: bot-a") {
		t.Fatalf("front matter = %q", rendered)
	}
}

// SetProfiles 会给没填 ID 的机器人补一个，所以每台机器人总有自己的一份文件，
// 不会退回一个共用文件——共用文件会让两台机器人互相覆盖人设。
func TestSyncSoulFilesAlwaysScopesToOneBotPerFile(t *testing.T) {
	dir := t.TempDir()
	runtime := soulTestRuntime(t, &soulTestSaver{},
		BotConfig{Platform: PlatformOneBotV11, BotAccount: "42", SystemPrompt: "甲的人设"},
		BotConfig{Platform: PlatformOneBotV11, BotAccount: "43", SystemPrompt: "乙的人设"})
	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	paths := map[string]bool{}
	for _, result := range results {
		if strings.TrimSpace(result.ProfileID) == "" {
			t.Fatalf("profile without an id reached the sync: %#v", result)
		}
		paths[result.Path] = true
	}
	if len(paths) != 2 {
		t.Fatalf("two bots shared one file: %#v", results)
	}
}
