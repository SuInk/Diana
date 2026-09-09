---
name: bot-protocol
description: Choose the current platform's supported protocol tools for group information and moderation, or Diana's configuration tool for reply behavior.
---

# Bot Protocol

Use the current event's platform and bot identity. Never substitute another bot, guess an account ID from a nickname, or emulate successful actions in prose.

## Platform Operations

- OneBot: load `onebot-v11`, then use `diana.onebot_v11` for group information, members and management. Standard queries include `get_group_info`, `get_group_member_list` and `get_group_member_info`. Ordinary members retain only the backend read allowlist; mutation and unknown extensions require the owner. A group administrator is not automatically the bot owner.
- Telegram and other platforms: use the available read-only `diana.group` adapter for `info`, `members` and `member`. Respect `member_list_complete`, `member_source` and warnings. Do not treat partial results as a full group list or invent unsupported moderation actions.
- Image-to-avatar matching is a local operation: use `diana.group` with `match_avatar`. Do not guess identity from an image.
- Do not bypass a denied action with shell, raw network requests, an alias or another tool.

## Diana Reply Settings

Platform protocol actions do not change Diana's participation settings. Use `diana.bot_config`:

- `{"operation":"get","scope":"group"}` reads the current group's effective behavior.
- `{"operation":"update","scope":"group","desire_level":"off"}` stops unsolicited participation in this group. It does not disable the bot, mute a platform account or stop responding to explicit requests.
- `desire_level` accepts `off`, `low`, `medium`, `high`, `max`. Lowering to `low` is different from turning participation off.
- `relevance_level` and `substance_level` accept `low`, `medium`, `high`, `max`, mapped to 40, 60, 80, 90. Higher thresholds are stricter.
- `cooldown_seconds` accepts 0 through 3600; 0 disables cooldown, not participation.
- Use `scope=bot` only when the owner explicitly wants to change the current bot's defaults. Group administrators can update only their current group after backend verification.
- Submit only requested fields. Changing desire must preserve score thresholds and cooldown. Do not submit legacy `chat_in_enabled`, `chat_in_level` or `natural_interjection_enabled` fields.
- Report success only after a successful save, using returned `participation`. On failure, report the error; never just promise to stay silent as if configuration changed.

The skill describes the workflow. Backend authorization and persistence are authoritative.
