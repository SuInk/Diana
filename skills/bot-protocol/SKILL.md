---
name: bot-protocol
description: Choose the current platform's supported protocol tools for group information and moderation, or Diana's configuration tool for reply behavior, member welcome and redacted diagnostics.
---

# Bot Protocol

Use the current event's platform and bot identity. Never substitute another bot, guess an account ID from a nickname, or emulate successful actions in prose.

## Platform Operations

- Group information, members and moderation go through `platform` (see the `platform` skill): `group_info`, `member_list` and `member_info` read; `mute`, `unmute` and `kick` are owner-only. A group administrator is not automatically the bot owner.
- Only when `platform` is not registered (a non-OneBot platform with the platform interface disabled), the read-only `group_directory` tool provides `info`, `members` and `member`. Respect `member_list_complete`, `member_source` and warnings. Do not treat partial results as a full group list or invent unsupported moderation actions.
- Image-to-avatar matching is a local operation: use the `match_avatar` tool, or `group_directory` with operation `match_avatar` where that tool is registered instead. Do not guess identity from an image.
- Do not bypass a denied action with shell, raw network requests, an alias or another tool.

## Diana Configuration

Use `bot_config` for Diana's settings and redacted diagnostics. Platform protocol actions do not change these settings:

- `{"operation":"get","scope":"group"}` reads the current group's effective behavior.
- `{"operation":"update","scope":"group","desire_level":"off"}` stops unsolicited participation in this group. It does not disable the bot, mute a platform account or stop responding to explicit requests.
- `desire_level` accepts `off`, `low`, `medium`, `high`, `max`. Lowering to `low` is different from turning participation off.
- `relevance_level` is the respond-to-questions switch: `on` or `off`. `chat_level` accepts `off`, `minimal`, `low`, `medium`, `high`, `always`; the four scored levels use thresholds 0.90, 0.70, 0.50, 0.30. Replies pass when the message is directed at Diana with the switch on, or when chat passes after cooldown. Do not submit `answerability_level` or `substance_level`: the separate answerability gate has been removed, and legacy numeric thresholds do not control this decision. Do not claim to change that removed gate.
- The chat branch pauses when, within the last 10 minutes and up to 30 context messages, Diana sent at least 3 messages and accounts for at least 25% of the messages, with more than one other speaker. `chat_level=always` bypasses this share limit; the question branch is unaffected. A rating response whose JSON cannot be parsed is retried once before the message is dropped. To disable only chat, set `chat_level=off`; to disable both unsolicited paths, also set `relevance_level=off`. Legacy `desire_level=off` also disables both paths.
- `cooldown_seconds` accepts 0 through 3600; 0 disables cooldown, not participation.
- Use `scope=bot` only when the owner explicitly wants to change the current bot's defaults. Group administrators can update only their current group after backend verification.
- Submit only requested fields. Changing desire must preserve other settings and cooldown. Do not submit legacy `chat_in_enabled`, `chat_in_level` or `natural_interjection_enabled` fields.
- Report success only after a successful save, using returned `participation`. On failure, report the error; never just promise to stay silent as if configuration changed.

### Built-in Member Welcome

- Turn welcome on or off with `{"operation":"update","welcome_enabled":true}` or `false`. In a group this changes only the current group's setting; `scope=bot` changes the current bot's defaults and is owner-only.
- Update `welcome_message`, `welcome_mode` (`fixed`, `template`, `llm`), `welcome_templates` (up to 50 texts, each up to 200 characters), or `welcome_llm_cooldown_seconds` (0–86400). Zero uses the bot or system default. Submit only fields the user requested; changing welcome must preserve participation settings.
- Requests such as “开启入群欢迎” configure the existing built-in feature. Repeated requests update the same setting. Do not create an `event_trigger` task to implement a welcome switch or change its wording. Use `event_trigger` only when the user explicitly requests an additional event task.
- Report the returned effective `welcome` settings only after persistence succeeds.

### Owner Diagnostics

- `{"operation":"get","section":"all","scope":"bot"}` reads redacted bot, runtime, LLM, installed skills/plugins and path information. Select `bot`, `runtime`, `llm`, `skills` or `paths` to limit the output. These sections are owner-only and read-only; the scope may be omitted or set to `bot`. `section=settings` is the default editable settings view.
- `llm_config` remains responsible for model assignment. There is no separate `config` tool or compatibility alias.

## Blocking One Person

Ignoring one person for good is not the same as lowering participation, and not the same as a platform mute. Use `reply_block`:

- `{"operation":"block","user_id":"123456"}` stops every reply to that account in the current group until it is unblocked. There is no timer; it is not the 30-minute automatic suppression.
- `{"operation":"unblock","user_id":"123456"}` restores replies, and `{"operation":"list"}` shows the current list.
- `scope=group` is the default inside a group and needs the owner or a backend-verified group administrator. `scope=bot` covers every group and private chat of this bot and is owner-only.
- `blocked_users` were set at the requested scope. `inherited_blocked_users` come from the bot-level list: they apply here too, but only the owner can lift them with `scope=bot`.
- Take `user_id` from an @ segment, from the quoted message's sender, or from a member lookup. Never guess it from a nickname. Blocking the bot owner or the bot's own account is refused.
- Blocking never mutes, kicks or removes anyone on the platform, and never deletes messages. Report success only after a successful save.

The skill describes the workflow. Backend authorization and persistence are authoritative.
