# MaiBot reply composition — Go port

Upstream: https://github.com/Mai-with-u/MaiBot

Revision: `f9ec3670a3638d6a4708ee5253f7ffe2838c9415`

Original work: MaiBot contributors. The Go files in this directory adapt the
reply prompt construction from:

- `src/chat/replyer/maisaka_generator_base.py`
- `prompts/zh-CN/maisaka_replyer.prompt`
- `src/config/official_configs.py`

`splitter.go` adapts the rule-based response splitter at
`b7f36825463413c47e61d44fe8a93427f6fe5a6f`:

- `src/chat/utils/utils.py` (`_split_into_sentence_segments`,
  `_merge_processed_segments_to_max_count`, `_finish_response`)
- `src/config/official_configs.py` (`ResponseSplitterConfig` defaults)

The adapted files retain the upstream GNU GPL version 3 license reproduced in
`LICENSE`. They are not covered solely by Diana's Limited Redistribution
License. No license change to unrelated Diana code is implied.

The port contains only the text replyer boundary: prompt assembly, visible chat
history, reply reference, target message, attention text and default reply
style, plus the rule-based sentence splitter used before delivery. It does not include MaiBot's planner, scheduler, database, model client,
delivery service, learning system or expression database.

Diana first completes its normal content and tool pass. The finished draft is
then supplied to this replyer as `reply_reference`, together with visible chat
history and the current target message. The replyer receives no tools, memory
blocks or Diana agent system prompt. Diana restores its delivery controls after
generation. If the replyer call fails, Diana sends the content draft instead.

Host adaptations are limited to explicit Go input structures, a supplied
clock, the host persona as identity input, non-QQ wording for group chats, clean
vocabulary and punctuation preferences, preservation of Diana mention/reply
delivery markers, and optional learned group style. The upstream participation
and planner decisions are not used; Diana keeps its own participation score and
reply target.
