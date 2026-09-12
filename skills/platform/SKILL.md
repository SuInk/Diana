---
name: platform
description: Read group information and members across platforms, and perform owner-only moderation (mute, unmute, kick) through the current platform. Use when a user explicitly asks to read group state or to mute, unmute, or remove a member.
---

# Platform Interface

Use `diana.platform` as the single cross-platform entry point for group information and moderation. Do not emulate an operation in prose and do not call it for ordinary conversation. The operations are platform-neutral verbs; the Go tool maps each to the current platform's native API (OneBot v11 or Telegram Bot API).

## Operations

- `group_info` — read the current group's live profile and member count.
- `member_info` — verify one account's current membership and role by `user_id`.
- `member_list` — fetch member candidates. Respect `member_list_complete`, `member_source`, `member_count_known` and `warnings`; on Telegram this is administrators plus observed accounts, never the full roster.
- `mute` — restrict one account for an explicit positive `duration` in seconds. Owner only.
- `unmute` — lift a restriction. Owner only.
- `kick` — remove one account. Optional `reject_add_request` also refuses re-joining. Owner only.

## Access Boundary

- Reads (`group_info`, `member_info`, `member_list`) are available to ordinary members.
- `mute`, `unmute` and `kick` are owner-only. A group administrator or the group owner who is not the bot owner is refused. The Go tool enforces this in `Run`; these instructions explain it but do not replace it.
- Moderation requires the bot itself to be a group administrator or owner on that platform. If the bot is a plain member the tool refuses without calling the destructive API and says so.
- Never target the bot owner or the bot's own account.
- Never bypass a denial with a raw request, an alias, a shell command, a browser request, or another tool.

## Targeting

Take `user_id` from an @ segment, from the quoted message's sender, or from a `member_info` / `member_list` result. Never guess an account ID from a nickname. When unsure, look the member up or ask.

## Platform Support

- OneBot v11: `group_info`, `member_info`, `member_list`, `mute` (`set_group_ban`, cap 2592000s / 30 days), `unmute`, `kick` (`set_group_kick`).
- Telegram: `group_info`, `member_info`, `member_list`, `mute` (`restrictChatMember` + `until_date`), `unmute`, `kick` (`banChatMember`, plus `unbanChatMember` when `reject_add_request` is false).
- Other platforms: reads use the platform's read-only adapter where available; moderation returns a clear "当前平台暂不支持此操作".

## Diana's Own Reply Behavior

Diana's participation is not a platform property. To stop unsolicited replies use `diana.bot_config` with `desire_level=off`, not a platform mute. To ignore one person's messages without a platform action use `diana.reply_block`. For local image-to-avatar matching use the read-only `diana.group` operation `match_avatar`.

## Result Handling

- `ok: true` means the platform returned successfully; inspect the payload for details.
- `access: owner_full` means owner authorization was used; `member_read_only` means the member read path was used.
- A permission error is final. Explain that moderation is owner-only and requires the bot to be a group administrator.
- A transport or platform error is not success. Report the operation and a concise error without exposing tokens or credentials, and never claim a mutation succeeded without a successful result.
