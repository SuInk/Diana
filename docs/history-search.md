# 历史检索与结果完整性

`chat_history` 返回的每条消息都是原文：正文、引用、图片描述不缩短，不抽样，不带前后文。
需要核对相邻回答时调用 `around`，传入命中的 `message_id`。
跨群展开还需传 `group_id`，要求跨群记忆已启用，且仍限于同一机器人命名空间。

## 条数与单次上限

`limit`、`before`、`after`、`ids` 都不设上限，模型要多少给多少。
工具结果统一受 Runner 的单次上限约束（`agent.ToolOutputMaxChars`，所有人、所有工具相同，做法照 Pi）。
放不下时按整条分页，不在条目内部裁剪：

- `search`、`range`：装不下的条目留给下一页，`next_cursor` 按实际返回条数推进。
- `ids`、`around`、`recent`、`recalls`：没装下的消息列进 `unreturned_message_ids`，再用 `ids` 分批读；`around` 先收离锚点远的一侧。

出现分页时，`message` 会写明单次上限、本页装下几条、覆盖的时间段（`page_window`）、还剩几条，以及怎么接着读。

## 排序与续查

- `order=newest`：默认按时间从新到旧检索。
- `order=oldest`：按时间从旧到新检索；未指定起始时间、hours 或 days 时查全部历史。
- `order=relevance`：保留关键词和语义候选的相关性融合，不承诺穷尽，也不支持精确分页。结果的 `total_is_exact` 和 `search_complete` 为 false。

时间排序在数据库执行，使用时间、创建时间和记录键作为稳定排序条件。
不要从已经截取的最新结果中反转顺序来寻找最早记录。

`search` 续查时原样传回 `next_cursor`，保持 `query`、`scope`、`order` 不变。
游标固定时间范围，并绑定当前会话和查询条件。
`range` 也支持游标；同一秒内有多条消息时，使用游标避免跳过剩余记录。
`next_from_time` 只在时间边界没有重复记录时提供，优先使用 `next_cursor`。

游标固定查询范围，并非数据库快照。查询期间若记录被回填、删除或更新索引，重新搜索可核对变化。

## 完整性字段

| 字段 | 含义 |
| --- | --- |
| `total` | 当前查询的匹配总数；相关性召回时只是已知候选数量，不是完整总数 |
| `total_is_exact` | 是否有可枚举的准确总数 |
| `offset` | 当前页之前已推进的条数 |
| `returned_count` | 本页实际返回的条数 |
| `remaining_count` | 还没返回的条数 |
| `has_more` | 是否还有未返回的消息；非穷尽召回仍保守标为有更多可能 |
| `page_window` | 有后续页时，本页第一条到最后一条的本地时间 |
| `search_complete` | 当前关键词与时间范围的可枚举结果是否已到末页，不保证事件事实完整 |
| `next_cursor` | 继续读取未返回命中的游标 |
| `unreturned_message_ids` | 不分页的操作里没装下的消息 |

未查完整个范围时，只能说“目前查到最早”，不能断言“最早就是”；
关键词检索完毕也不能证明其他表达方式下不存在更早证据。
