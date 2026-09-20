import type { BotProfileConfig } from "./api";

// Credentials are deliberately excluded: sharing is an explicit choice, never
// inferred from matching tokens or implemented by copying a saved secret.
export function websocketConnectionKey(profile: Partial<BotProfileConfig>): string {
  if (profile.connection_profile_id || (profile.platform && profile.platform !== "onebot-v11")) return "";
  const mode = profile.onebot_transport || "reverse_ws";
  if (mode !== "reverse_ws" && mode !== "forward_ws") return "";
  const endpoint = mode === "forward_ws" ? profile.onebot_ws_endpoint : profile.onebot_reverse_ws_endpoint;
  try {
    const url = new URL((endpoint || "").trim());
    if (!["ws:", "wss:"].includes(url.protocol) || !url.hostname) return "";
    return `${mode}|${url.protocol}//${url.host}${url.pathname || "/"}${url.search}`;
  } catch {
    return "";
  }
}

export function findWebSocketConnectionConflict(
  profile: Partial<BotProfileConfig>, profiles: BotProfileConfig[]
): BotProfileConfig | undefined {
  const key = websocketConnectionKey(profile);
  if (!key) return undefined;
  return profiles.find((source) => source.id && source.id !== profile.id && websocketConnectionKey(source) === key);
}

// 复用同一条连接的机器人共用一个平台账号：一条群消息会交给每一台，各自按自己的
// 群准入决定回不回。谁管哪个群因此散在各台自己的白名单里，跨机器人看不到全貌，
// 两台都放行同一个群就会在那个群里回两遍。下面这组函数把这张表拼起来。
export interface ConnectionGroupMember {
  id: string;
  name: string;
  enabled: boolean;
  /** whitelist 只收 allowedGroups 里的群，blacklist（默认）收 disabledGroups 以外的所有群。 */
  whitelist: boolean;
  allowedGroups: string[];
  disabledGroups: string[];
  /** 正在编辑、尚未保存的那一台。 */
  editing?: boolean;
}

/** connectionGroupID 返回这台机器人所在连接组的标识：复用方用来源 ID，来源用自己的 ID。 */
export function connectionGroupID(profile: Partial<BotProfileConfig>): string {
  return (profile.connection_profile_id || profile.id || "").trim();
}

function toMember(profile: BotProfileConfig, editing = false): ConnectionGroupMember {
  return {
    id: (profile.id || "").trim(),
    name: profile.name || "未命名机器人",
    enabled: profile.enabled !== false,
    whitelist: profile.group_admission?.mode === "whitelist",
    allowedGroups: [...(profile.group_admission?.allowed_groups ?? [])].map((group) => group.trim()).filter(Boolean),
    disabledGroups: [...(profile.disabled_groups ?? [])].map((group) => group.trim()).filter(Boolean),
    editing
  };
}

/**
 * connectionGroupMembers 列出和这台机器人共用一条连接的所有启用机器人（含它自己）。
 * draft 是正在编辑、还没保存的那一份，它会顶替列表里的同一台，提示才跟得上手上的改动。
 * 只有一台时返回空：没有复用就没有归属问题。
 */
export function connectionGroupMembers(
  draft: Partial<BotProfileConfig>, profiles: BotProfileConfig[]
): ConnectionGroupMember[] {
  const connection = connectionGroupID(draft);
  if (!connection) return [];
  const draftID = (draft.id || "").trim();
  const members = profiles
    .filter((profile) => profile.id && profile.id.trim() !== draftID && connectionGroupID(profile) === connection)
    .map((profile) => toMember(profile));
  if (draft.enabled !== false) {
    // 新建还没保存的那台没有 ID，成员表按名字显示，不借用来源的 ID 占位。
    members.push(toMember({ ...draft, id: draftID } as BotProfileConfig, true));
  }
  const enabled = members.filter((member) => member.enabled);
  return enabled.length > 1 ? enabled : [];
}

function coversGroup(member: ConnectionGroupMember, groupID: string): boolean {
  if (member.disabledGroups.includes(groupID)) return false;
  return member.whitelist ? member.allowedGroups.includes(groupID) : true;
}

export interface GroupRoutingOverlap {
  groupID: string;
  members: ConnectionGroupMember[];
}

/**
 * groupRoutingOverlaps 找出会被同一条连接上多台机器人同时接管的群。
 * 只检查白名单点名过的群：黑名单模式覆盖的群是无穷多个，列不出来，那种
 * 「谁都不限群」的配置交给 openScopeMembers 整句说，不必拎出某一个群号——
 * 切回黑名单后白名单里残留的群号也因此不再被当成冲突点名。
 */
export function groupRoutingOverlaps(members: ConnectionGroupMember[]): GroupRoutingOverlap[] {
  const named = [...new Set(members.filter((member) => member.whitelist).flatMap((member) => member.allowedGroups))].sort();
  return named
    .map((groupID) => ({ groupID, members: members.filter((member) => coversGroup(member, groupID)) }))
    .filter((overlap) => overlap.members.length > 1);
}

/** openScopeMembers 返回不限群的机器人：它们收这条连接上的每一个群。 */
export function openScopeMembers(members: ConnectionGroupMember[]): ConnectionGroupMember[] {
  return members.filter((member) => !member.whitelist);
}
