// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Older backends do not return reply_tone_default. Keep their last built-in
// value here so the editor still shows real text instead of an empty box.
export const FALLBACK_REPLY_TONE = [
  "你的风格平淡简短，可以参考贴吧的回复风格。不滥用比喻或者生硬句子。视情况省略主语或者进行倒装，风格较为随意。",
  "不说粗口、谐音粗口或辱骂，包括用‘草’‘卧槽’‘我靠’‘tmd’表达惊讶；不模仿历史中的脏话。正常词语、代码和客观释义不受影响，不解释这条规则。",
  "不要使用‘（笑’‘（笑）’‘(笑’之类括号笑声或文字旁白，也不要用只开不合的「（」接一段补充的话，不要照抄聊天记录里的这些旧口癖；觉得好笑时直接用自然措辞表达，要补充就另起一句。",
  "日常短回复的最后一句通常不加中文或英文句号，问号、感叹号和省略号按语义自然使用。技术说明、多句长回答、代码、网址、文件路径和引用原文的标点照常保留，不要机械删除句号。"
].join("\n");

export function normalizeReplyTone(value: string | undefined): string {
  return (value ?? "").replace(/\r\n?/g, "\n").trim();
}

export function effectiveReplyTone(custom: string | undefined, builtin: string | undefined): string {
  return normalizeReplyTone(custom) || normalizeReplyTone(builtin) || FALLBACK_REPLY_TONE;
}

// Saving the unchanged built-in as an empty override lets future releases
// update the default without freezing every existing robot on an old copy.
export function replyToneOverride(draft: string, builtin: string | undefined): string {
  const normalized = normalizeReplyTone(draft);
  const effectiveBuiltin = normalizeReplyTone(builtin) || FALLBACK_REPLY_TONE;
  return normalized === effectiveBuiltin ? "" : normalized;
}
