# Issue 授权：按人、按群，以及群身份要求

`github` 工具的写操作（create / update / comment / review / close / reopen）不看会话类型，只看授权名单。名单分两种口径。

## 按用户：授权跟着人走

`issue_manager_user_access`、`issue_draft_user_access`、`code_reader_user_access`（以及回退用的老字段 `user_repository_access`）都按 `用户 ID = owner/repo, owner/repo` 填写。判定时取的是 `event.UserID`，与会话类型无关——**同一个人在私聊和群聊里权限一样**。想让某个群友能操作某个仓库，填他的用户 ID 就够了，不必放开整个群。

WebUI 里这一栏原来标成「私聊 / 群聊」，沿用了通知对象那套说法，容易读成"只在私聊生效"。现在标为「按用户 / 按群」，输入框提示也从"私聊用户 ID"改成"用户 ID"。存储值没变。

## 按群：整群授权，可以再限定群身份

`issue_manager_group_access`、`issue_draft_group_access`（老字段 `group_repository_access`）按 `群 ID = owner/repo, owner/repo` 填写，授权范围是该群成员。

默认群里所有人都算数——这是老行为，不写后缀的配置读进来语义不变。要收窄就在仓库后面加身份要求：

| 写法 | 谁能用 |
| --- | --- |
| `owner/repo` | 群里所有成员 |
| `owner/repo#group_admin` | 群主和群管理员 |
| `owner/repo#group_owner` | 只有群主 |

机器人主人在哪一档都通行：主人的权限本来就不经过这份名单。

## 两个"管理"不是一回事

- **Issue 管理人员**：Diana 这个插件的授权角色，决定谁能直接写 GitHub、谁负责确认草稿。由上面那几项名单决定。
- **群主 / 群管理员 / 群成员**：聊天平台给的群内身份，由平台决定，Diana 只是读。`#group_admin` 这类后缀说的是它。

一条按群授权写成 `#group_admin`，意思是"这个群里，群主和群管理员算作 Issue 管理人员"，不是"群管理员就是 Issue 管理人员"。要单独放行某个人，用按用户的授权。

管理人员自动也是草稿人（`repositoryPublishEffectiveAccess` 里显式合并），不必重复添加。两条授权覆盖同一个群 + 仓库时，草稿侧按更宽的那条算——否则把管理人员收窄到群管理员，会连带把本来放给全体成员的草稿权一起收走。

## 身份怎么取，取不到怎么办

先看事件自带的 `SenderRole`（OneBot 每条群消息都带 `sender.role`，Telegram 的 `creator`/`administrator`、钉钉的 isAdmin 都由 `NormalizeGroupRole` 收敛成同一套取值），拿不到再回查一次群成员信息。

查不到身份时按最严处理：除「所有成员」外一律拒绝，并在回复里说明是平台没提供群身份，而不是仓库没配。身份够不上时同样明确拒绝，提示里写清这条授权要求什么身份、当前身份是什么，以及"想单独放行就用按用户的授权"。

身份查询是惰性的：一条带后缀的授权都没有时，不会为此多打任何平台接口。`github_watch` 的管理清单属于每条消息都要走的热路径，那里只认事件自带的身份，不额外回查。
