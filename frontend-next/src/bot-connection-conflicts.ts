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
