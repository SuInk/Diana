---
name: platform
description: Read group information and members across platforms, recall the bot's own recently sent messages, and perform owner-only moderation (mute, unmute, kick, group announcements, essence/pinned messages, member card and title, whole-group mute, recalling members' messages) through the current platform. Use when a user explicitly asks to read group state, when the bot must take back something it just sent, or to moderate the group.
---

# Platform Interface

Use `platform` as the single cross-platform entry point for group information and moderation. Do not emulate an operation in prose and do not call it for ordinary conversation. The operations are platform-neutral verbs; the Go tool maps each to the current platform's native API (OneBot v11 or Telegram Bot API).

## Operations

- `group_info` — read the current group's live profile and member count.
- `member_info` — verify one account's current membership and role by `user_id`.
- `member_list` — fetch member candidates. Respect `member_list_complete`, `member_source`, `member_count_known` and `warnings`; on Telegram this is administrators plus observed accounts, never the full roster.
- `recall` — delete one message the bot itself sent. Optional `message_id`; omitted means the bot's most recent own message in this session. Available to everyone.
- `mute` — restrict one account for an explicit positive `duration` in seconds. Owner only.
- `unmute` — lift a restriction. Owner only.
- `kick` — remove one account. Optional `reject_add_request` also refuses re-joining. Owner only.
- `announce_list` — list group announcements (with their `notice_id`). Read-only.
- `announce` — publish a group announcement from `content`. Owner only.
- `announce_delete` — delete one announcement by `notice_id` from `announce_list`. Owner only.
- `essence_set` / `essence_unset` — mark or unmark a message as essence (pinned on Telegram). `message_id` from history, or the quoted message when omitted. Owner only.
- `set_card` — change a member's group card; empty `card` clears it. Owner only.
- `set_title` — change a member's special title; empty `title` clears it. On QQ only the group owner can do this. Owner only.
- `mute_all` / `unmute_all` — turn whole-group mute on or off. Owner only.
- `recall_messages` — recall members' messages to clean up floods or ads: one `message_id` from history (or the quoted message), or a `user_id` plus `count` (default 10, max 50) to recall that account's most recent messages seen in this session. Reports how many were recalled and how many failed. Owner only.

## Recalling Your Own Message

Saying "当我没说" or "收回刚才那句" deletes nothing — the wrong message stays in the group and most readers only see the first one. When a correction is needed, call `recall`, then send the corrected content as a normal message.

- `recall` only ever targets a message the bot itself sent. `message_id` must come from the bot's own message in the session history; never guess one from the text, and never pass another member's or another bot's message id. The Go tool refuses those.
- Omitting `message_id` recalls the bot's most recent own message. Use that only when the message to take back really is the last one.
- If the call fails — unsupported platform, past the platform's recall window, missing permission, message already gone — the original message is still there. Say plainly that it could not be recalled, then send the correction. Never describe a failed or skipped recall as a successful one.
- Recalling is not a way to hide a conversation. Use it for the bot's own mistakes, not to erase a record someone asked to keep.

## Access Boundary

- Reads (`group_info`, `member_info`, `member_list`) are available to ordinary members.
- `recall` is available to ordinary members, because it only affects the bot's own output.
- Every moderation operation (`mute`, `unmute`, `kick`, announcements, essence, card, title, whole-group mute, `recall_messages`) is owner-only. A group administrator or the group owner who is not the bot owner is refused. The Go tool enforces this in `Run`; these instructions explain it but do not replace it.
- Moderation requires the bot itself to be a group administrator or owner on that platform. If the bot is a plain member the tool refuses without calling the destructive API and says so.
- Never target the bot owner or the bot's own account.
- Never bypass a denial with a raw request, an alias, a shell command, a browser request, or another tool.

## Targeting

Take `user_id` from an @ segment, from the quoted message's sender, or from a `member_info` / `member_list` result. Never guess an account ID from a nickname. When unsure, look the member up or ask.

## Platform Support

- OneBot v11 (NapCat, LLOneBot, Lagrange): `group_info`, `member_info`, `member_list`, `recall` (`delete_msg`; QQ only allows this within a short window after sending), `mute` (`set_group_ban`, cap 2592000s / 30 days), `unmute`, `kick` (`set_group_kick`), `announce_list` / `announce` / `announce_delete` (`_get_group_notice` / `_send_group_notice` / `_del_group_notice`), `essence_set` / `essence_unset` (`set_essence_msg` / `delete_essence_msg`), `set_card` (`set_group_card`), `set_title` (`set_group_special_title`, group owner only), `mute_all` / `unmute_all` (`set_group_whole_ban`), `recall_messages` (`delete_msg` per message; admins can recall members' messages but not the group owner's or other admins').
- Telegram: `group_info`, `member_info`, `member_list`, `recall` (`deleteMessage`, 48 hours for the bot's own messages), `mute` (`restrictChatMember` + `until_date`), `unmute`, `kick` (`banChatMember`, plus `unbanChatMember` when `reject_add_request` is false), `essence_set` / `essence_unset` (`pinChatMessage` / `unpinChatMessage`), `set_title` (`setChatAdministratorCustomTitle`, only for administrators the bot promoted), `mute_all` / `unmute_all` (`setChatPermissions`), `recall_messages` (`deleteMessage`, 48 hours). Telegram has no group announcements and no per-group member card, so `announce*` and `set_card` are unsupported.
- Other platforms: reads use the platform's read-only adapter where available; `recall` and moderation return a clear "当前平台暂不支持此操作".

## Diana's Own Reply Behavior

Diana's participation is not a platform property. To stop unsolicited replies use `bot_config` with `desire_level=off`, not a platform mute. To ignore one person's messages without a platform action use `reply_block`. For local image-to-avatar matching use the read-only `match_avatar` tool.

## Result Handling

- `ok: true` means the platform returned successfully; inspect the payload for details.
- `access: owner_full` means owner authorization was used; `member_read_only` means the member read path was used.
- A permission error is final. Explain that moderation is owner-only and requires the bot to be a group administrator.
- A transport or platform error is not success. Report the operation and a concise error without exposing tokens or credentials, and never claim a mutation succeeded without a successful result.
