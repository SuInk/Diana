const cqLabels: Record<string, string> = {
  image: "图片", face: "表情", mface: "表情包", reply: "回复消息",
  record: "语音", video: "视频", file: "文件", forward: "合并转发",
  share: "分享", contact: "联系人", location: "位置", music: "音乐",
  json: "卡片消息", xml: "卡片消息", poke: "戳一戳", dice: "骰子",
  rps: "猜拳", gift: "礼物", redbag: "红包"
};

function decodeEntities(value: string): string {
  let current = value;
  for (let pass = 0; pass < 3; pass += 1) {
    const textarea = document.createElement("textarea");
    textarea.innerHTML = current;
    const decoded = textarea.value;
    if (decoded === current) break;
    current = decoded;
  }
  return current;
}

function cqParam(payload: string, key: string): string {
  for (const part of payload.split(",").slice(1)) {
    const separator = part.indexOf("=");
    if (separator < 0 || part.slice(0, separator) !== key) continue;
    return decodeEntities(part.slice(separator + 1));
  }
  return "";
}

function readableCQ(payload: string): string {
  const type = payload.split(",", 1)[0]?.trim().toLowerCase() ?? "";
  if (type === "at") {
    const target = cqParam(payload, "qq");
    if (target === "all") return "@全体成员";
    if (!target) return "[提及成员]";
    // 后端渲染事件正文时已经补过昵称；这里只兜底 CQ 码自带昵称的情况。
    const name = (cqParam(payload, "name") || cqParam(payload, "card") || cqParam(payload, "nickname")).trim();
    return name ? `@${name}（${target}）` : `@${target}`;
  }
  if (type === "text") return cqParam(payload, "text");
  if (type === "file") {
    const name = cqParam(payload, "name") || cqParam(payload, "file");
    return name && !/^https?:\/\//i.test(name) ? `[文件: ${name}]` : "[文件]";
  }
  return `[${cqLabels[type] ?? `消息组件:${type || "未知"}`}]`;
}

// [diana-reply:ID] 是入站给模型、出站给适配器用的引用标记，不该出现在界面上。事件
// 列表的正文由后端渲染成「[回复 某人：原话]」，但实时事件流和早先存下的正文里还会
// 带着它——这里只能退回一个「[回复]」：手上只有一个消息 ID，说不出回的是谁。
const replyMarkerPattern = /\[(?:diana-reply|回复):(-?[0-9]{4,19})\]/g;

// 早先存下的正文里，合并转发写成「[合并转发:<一长串 base64 resid>]」。那串东西没有
// 任何一处会再读它，在列表里只是把正文挤出屏幕。新写入的正文后端已经渲染成「[合并
// 转发]」了，这里只管旧数据。
const forwardMarkerPattern = /\[合并转发:[^\]]{8,}\]/g;

export function displayMessageText(value?: string): string {
  if (!value) return "";
  const rendered = value
    .replace(/\[CQ:([^\]]+)\]/gi, (_match, payload: string) => readableCQ(payload))
    .replace(replyMarkerPattern, "[回复] ")
    .replace(forwardMarkerPattern, "[合并转发]");
  return decodeEntities(rendered)
    .replace(/[ \t]+\n/g, "\n")
    .replace(/[ \t]{2,}/g, " ")
    .trim();
}

export function displayChatIdentity(name?: string, qq?: string): string {
  const normalizedName = name?.trim() ?? "";
  const normalizedAccount = qq?.trim() ?? "";
  if (normalizedName && normalizedAccount) return `${normalizedName}（${normalizedAccount}）`;
  return normalizedName || normalizedAccount;
}
