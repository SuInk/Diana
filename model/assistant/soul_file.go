// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// soul.md：把人设正文摆成每台机器人一份的文件，给人写。
//
// 人设正文几千字，塞在 WebUI 的文本框里难写、难 diff、难进版本库。但它同时是
// 系统提示词稳定头部的第一行，运行时每条消息都要读，还要支持分群覆盖和 WebUI
// 随时改——所以文件不能当运行时的真相源，否则同一段人设会有两个互相覆盖的来源
// （项目对 bot/llm 配置一贯的规矩就是「数据库是唯一真相源，YAML 只在库空时播种」，
// 见 config.example.yaml）。
//
// 这里的定位因此是导入/导出源：文件改了导进数据库，数据库改了导出到文件，两边
// 同时改了不猜，保住数据库那份并把文件那份留在一旁。权威副本始终在数据库，
// WebUI、分群覆盖、人设迁移逻辑都不用动。
const (
	// SoulFileSuffix 是人设文件的扩展名。文件名是机器人 ID：ID 不会因为改名而变，
	// 改个显示名不该让同步认不出这是同一台机器人的人设。
	SoulFileSuffix = ".md"
	// soulFrontMatterFence 分隔元信息和正文。
	soulFrontMatterFence = "---"
	// soulConflictTimeLayout 用在冲突副本的文件名上。
	soulConflictTimeLayout = "20060102-150405"
)

// SoulSyncAction 说明这一台机器人这次同步做了什么。
type SoulSyncAction string

const (
	// SoulSyncCreated 是文件不存在、按数据库里的人设新建。
	SoulSyncCreated SoulSyncAction = "created"
	// SoulSyncImported 是文件被人改过，导入数据库。
	SoulSyncImported SoulSyncAction = "imported"
	// SoulSyncExported 是数据库里的人设变了（WebUI 改的），写回文件。
	SoulSyncExported SoulSyncAction = "exported"
	// SoulSyncUnchanged 是两边一致，什么都没做。
	SoulSyncUnchanged SoulSyncAction = "unchanged"
	// SoulSyncConflict 是两边都改了。保住数据库那份，文件那份另存一旁。
	SoulSyncConflict SoulSyncAction = "conflict"
)

// SoulSyncResult 是一台机器人的同步结果。
type SoulSyncResult struct {
	ProfileID string         `json:"profile_id"`
	Name      string         `json:"name,omitempty"`
	Path      string         `json:"path"`
	Action    SoulSyncAction `json:"action"`
	// ConflictPath 只在 action=conflict 时有值：文件那份被另存到了哪。
	ConflictPath string `json:"conflict_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

// soulFile 是一份解析出来的人设文件。
type soulFile struct {
	// SyncedHash 是上次同步时正文的哈希。人改文件不会顺手改它，所以它和当前正文
	// 的哈希一比，就知道文件有没有被人动过；和数据库里人设的哈希一比，就知道
	// WebUI 有没有改过。没有这一行时无从判断，按「首次接管」处理。
	SyncedHash string
	Body       string
}

// SyncSoulFiles 同步目录下的人设文件，每台机器人一份 <机器人 ID>.md。
//
// dir 为空表示没启用文件同步，直接返回。目录不存在时创建：第一次跑起来应该看到
// 一份份导出好的人设，而不是一个空目录加一行日志。
func (r *Runtime) SyncSoulFiles(dir string) ([]SoulSyncResult, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create souls dir: %w", err)
	}
	profiles := r.ProfileConfigs()
	results := make([]SoulSyncResult, 0, len(profiles))
	for _, profile := range profiles {
		profileID := strings.TrimSpace(profile.ID)
		if profileID == "" {
			// 正常路径上 SetProfiles 会给每台机器人补上 ID，所以这里是兜底：没有 ID
			// 就跳过，不退回一个共用文件——共用文件会让两台机器人互相覆盖人设。
			continue
		}
		result, err := r.syncSoulFile(dir, profile)
		if err != nil {
			// 一台机器人的人设文件坏了不该挡住其他几台，也不该挡住启动。
			log.Printf("diana soul file sync failed: profile=%s err=%v", profileID, err)
			results = append(results, SoulSyncResult{
				ProfileID: profileID, Name: profile.Name, Path: SoulFilePath(dir, profileID),
				Action: SoulSyncUnchanged, Message: err.Error(),
			})
			continue
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].ProfileID < results[j].ProfileID })
	return results, nil
}

// SoulFilePath 返回一台机器人的人设文件路径。
func SoulFilePath(dir, profileID string) string {
	return filepath.Join(dir, strings.TrimSpace(profileID)+SoulFileSuffix)
}

func (r *Runtime) syncSoulFile(dir string, profile BotConfig) (SoulSyncResult, error) {
	profileID := strings.TrimSpace(profile.ID)
	path := SoulFilePath(dir, profileID)
	result := SoulSyncResult{ProfileID: profileID, Name: strings.TrimSpace(profile.Name), Path: path}
	persona := strings.TrimSpace(profile.SystemPrompt)
	personaHash := soulHash(persona)

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := writeSoulFile(path, profile, persona, personaHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncCreated
		return result, nil
	}
	if err != nil {
		return result, err
	}
	parsed := parseSoulFile(string(raw))
	fileHash := soulHash(parsed.Body)

	switch {
	case strings.TrimSpace(parsed.Body) == "":
		// 空正文不当成「把人设清空」：更可能是编辑器写坏了、文件被截断，或者有人
		// 刚建了个空文件。按数据库那份导出回去，不走冲突分支——冲突副本存一份空
		// 文件只是噪音。
		if err := writeSoulFile(path, profile, persona, personaHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncExported
		result.Message = "文件正文为空，未导入；已按数据库里的人设重写文件"
		return result, nil

	case fileHash == personaHash:
		// 正文一致。元信息里的哈希可能还停在上一轮（比如上次是手工建的文件），
		// 顺手刷新一次，下一轮才判断得出谁改的。
		if parsed.SyncedHash != personaHash {
			if err := writeSoulFile(path, profile, persona, personaHash); err != nil {
				return result, err
			}
		}
		result.Action = SoulSyncUnchanged
		return result, nil

	case parsed.SyncedHash == "":
		// 没有同步记录：这份文件是人手工放进来的，用途就是把人设喂进去。
		// 首次接管以文件为准，之后才按哈希判断谁改的。
		if err := r.importSoul(profileID, parsed.Body); err != nil {
			return result, err
		}
		if err := writeSoulFile(path, profile, parsed.Body, fileHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncImported
		result.Message = "首次接管：以文件内容为准导入数据库"
		return result, nil

	case parsed.SyncedHash == personaHash:
		// 只有文件变了：人改了 soul.md。
		if err := r.importSoul(profileID, parsed.Body); err != nil {
			return result, err
		}
		if err := writeSoulFile(path, profile, parsed.Body, fileHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncImported
		return result, nil

	case parsed.SyncedHash == fileHash:
		// 只有数据库变了：人在 WebUI 里改的。
		if err := writeSoulFile(path, profile, persona, personaHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncExported
		return result, nil

	default:
		// 两边都变了。不猜哪边更新：数据库是运行时真相源，保住它；文件那份原样
		// 另存一旁，人自己来合。直接覆盖会悄悄吃掉一份手写的人设。
		conflictPath := soulConflictPath(dir, profileID, time.Now())
		if err := os.WriteFile(conflictPath, []byte(parsed.Body+"\n"), 0o644); err != nil {
			return result, err
		}
		if err := writeSoulFile(path, profile, persona, personaHash); err != nil {
			return result, err
		}
		result.Action = SoulSyncConflict
		result.ConflictPath = conflictPath
		result.Message = "文件和 WebUI 都改过：保留数据库里的人设，文件那份已另存"
		log.Printf("diana soul file conflict: profile=%s kept database persona, file copy saved to %s", profileID, conflictPath)
		return result, nil
	}
}

// importSoul 把文件里的人设写进数据库，走和聊天里改配置同一个入口：锁的用法、
// 落盘顺序和内存回写都不必再实现一遍。
func (r *Runtime) importSoul(profileID, persona string) error {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		// 空文件不当成「把人设清空」：更可能是编辑器写坏了或文件被截断。
		return fmt.Errorf("人设文件正文为空，未导入")
	}
	r.mu.RLock()
	saver := r.configSaver
	r.mu.RUnlock()
	_, err := r.commitProfileChange(profileID, func(cfg *BotConfig) error {
		cfg.SystemPrompt = persona
		return nil
	}, func(cfg BotConfig) error {
		if saver == nil {
			return fmt.Errorf("当前部署没有接入机器人配置存储，人设无法落库")
		}
		saver.SaveBotConfig(cfg)
		return nil
	})
	return err
}

func soulConflictPath(dir, profileID string, now time.Time) string {
	return filepath.Join(dir, strings.TrimSpace(profileID)+".conflict-"+now.Format(soulConflictTimeLayout)+SoulFileSuffix)
}

// writeSoulFile 原子写入：先写同目录临时文件再改名，别让同步过程中的崩溃留下
// 半截人设——那正是下一次启动要拿来导入数据库的内容。
func writeSoulFile(path string, profile BotConfig, body, hash string) error {
	content := renderSoulFile(profile, body, hash, time.Now())
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.WriteString(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// renderSoulFile 渲染整份文件：元信息 + 正文。
//
// 元信息只放同步要用的东西和一句给人看的说明。机器人名字写在里面是为了让一个
// 目录里的十来份 <ID>.md 认得出谁是谁，它不参与同步判断，改名不会触发导入。
func renderSoulFile(profile BotConfig, body, hash string, now time.Time) string {
	var builder strings.Builder
	builder.WriteString(soulFrontMatterFence + "\n")
	if name := strings.TrimSpace(profile.Name); name != "" {
		builder.WriteString("bot: " + name + "\n")
	}
	builder.WriteString("profile_id: " + strings.TrimSpace(profile.ID) + "\n")
	builder.WriteString("synced_at: " + now.Format(time.RFC3339) + "\n")
	builder.WriteString("synced_hash: " + hash + "\n")
	builder.WriteString(soulFrontMatterFence + "\n")
	builder.WriteString("\n")
	builder.WriteString("<!-- 这一段之下是人设正文，改完保存即可：下次同步会导入数据库。\n")
	builder.WriteString("     synced_hash 由同步维护，不要手改；它用来分辨这次是你改了文件还是在 WebUI 里改了人设。 -->\n\n")
	builder.WriteString(strings.TrimSpace(body))
	builder.WriteString("\n")
	return builder.String()
}

// parseSoulFile 拆出元信息和正文。没有元信息的文件整份都是正文——手写一份纯
// Markdown 扔进去就该能用，不该要求人先补一段 front matter。
func parseSoulFile(raw string) soulFile {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	trimmed := strings.TrimLeft(normalized, "\n ")
	if !strings.HasPrefix(trimmed, soulFrontMatterFence+"\n") {
		return soulFile{Body: strings.TrimSpace(stripSoulComments(normalized))}
	}
	rest := strings.TrimPrefix(trimmed, soulFrontMatterFence+"\n")
	end := strings.Index(rest, "\n"+soulFrontMatterFence)
	if end < 0 {
		return soulFile{Body: strings.TrimSpace(stripSoulComments(normalized))}
	}
	header := rest[:end]
	body := rest[end+len("\n"+soulFrontMatterFence):]
	body = strings.TrimPrefix(body, "\n")
	parsed := soulFile{Body: strings.TrimSpace(stripSoulComments(body))}
	for _, line := range strings.Split(header, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "synced_hash" {
			parsed.SyncedHash = strings.TrimSpace(value)
		}
	}
	return parsed
}

// stripSoulComments 去掉我们自己写进去的 HTML 注释说明。它是给人看的提示，不是
// 人设的一部分；留着会被当成正文导进数据库，然后每轮都发给模型。
func stripSoulComments(body string) string {
	for {
		start := strings.Index(body, "<!--")
		if start < 0 {
			return body
		}
		end := strings.Index(body[start:], "-->")
		if end < 0 {
			return body
		}
		body = body[:start] + body[start+end+len("-->"):]
	}
}

func soulHash(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
