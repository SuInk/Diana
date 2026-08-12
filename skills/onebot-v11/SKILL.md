---
name: onebot-v11
description: Safely inspect or operate the current QQ bot through OneBot v11. Use when a user explicitly asks to read OneBot or QQ state, query protocol data, perform a QQ management action, or invoke a standard or implementation-specific OneBot action.
---

# OneBot v11

Use `diana.onebot_v11` as the only protocol entry point. Do not emulate an action in prose and do not call it for ordinary conversation.

## Access Boundary

- The owner may call every OneBot v11 action, including NapCat, Lagrange, and go-cqhttp extensions.
- A non-owner may call only the tool's built-in read-only allowlist. Unknown actions fail closed even when their names start with `get_`.
- Credential actions such as `get_cookies`, `get_csrf_token`, and `get_credentials` are owner-only.
- Never bypass a denial with an alias, async suffix, raw WebSocket request, shell command, browser request, or another tool.
- The Go tool enforces this boundary. These instructions explain it but do not replace it.

## Workflow

1. Confirm that the current user explicitly asked for the QQ or OneBot operation.
2. Choose the narrowest action and parameters. Reuse IDs supplied by the user or trusted conversation context; never invent an ID.
3. Call `diana.onebot_v11` with `{"action":"...","params":{...}}`.
4. Treat the returned JSON as authoritative. Report errors plainly and never claim a mutation succeeded without a successful tool result.
5. Avoid repeating a mutating action after a timeout because the first call may already have taken effect. Read state first when a safe verification action exists.

## Read-Only Actions for Members

The member allowlist follows the public OneBot v11 API:

- Messages and forwards: `get_msg`, `get_forward_msg`
- Identity and contacts: `get_login_info`, `get_stranger_info`, `get_friend_list`
- Groups: `get_group_info`, `get_group_list`, `get_group_member_info`, `get_group_member_list`, `get_group_honor_info`
- Media retrieval: `get_record`, `get_image`
- Capability and runtime state: `can_send_image`, `can_send_record`, `get_status`, `get_version_info`

The `_async` and `_rate_limited` variants of these actions retain the same read-only classification. All other standard actions and every implementation-specific action require the owner.

## Owner Operations

Owner-only operations include sending or deleting messages, likes, group moderation and configuration, friend or group request handling, restart and cache actions, credential reads, hidden quick operations, and implementation extensions. Pass their documented parameter object without renaming fields.

Examples:

- Read group information: `{"action":"get_group_info","params":{"group_id":123456}}`
- Owner renames a group: `{"action":"set_group_name","params":{"group_id":123456,"group_name":"New name"}}`
- Owner invokes an extension: `{"action":"get_group_msg_history","params":{"group_id":123456,"message_seq":0,"count":20}}`

## Result Handling

- `ok: true` means the adapter returned successfully; inspect `data` for protocol-level details.
- `access: owner_full` means owner authorization was used; `member_read_only` means the member allowlist was used.
- A permission error is final. Explain that ordinary members have read-only OneBot access.
- A transport or adapter error is not success. Include the action name and a concise error summary without exposing cookies, tokens, or credential values.
