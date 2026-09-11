export interface ParticipationPreferences {
	 relevance_level?: string;
	 chat_level?: string;
	 answerability_level?: string;
  desire: number;
  social: number;
  followup: number;
  restraint: number;
  information: number;
  cooldown_seconds?: number;
  relevance_threshold?: number;
  substance_threshold?: number;
}

export const defaultParticipationCooldownSeconds = 30;
export const defaultParticipationScoreThreshold = 60;

// participationLevelThresholds 镜像 model/assistant/participation_single_score.go 里 ratingPasses
// 的门槛表：模型给出的分数 >= 门槛才算这一项达标，off 不参与判断，always 跳过该项门槛。
// 注意档位和门槛是反的：参与度档位越高，分数门槛越低，所以「极低」最严（0.90）、「极高」最松（0.10）。
// 这里的数字是后端判断口径的复述，改动必须和 ratingPasses 同步，否则界面会骗人。
export const participationLevelThresholds: Record<string, number | null> = {
  off: null,
  minimal: 0.9,
  low: 0.7,
  medium: 0.5,
  high: 0.3,
  extreme: 0.1,
  always: null,
};

export const participationLevelNames: Record<string, string> = { off: "关", minimal: "极低", low: "低", medium: "中", high: "高", extreme: "极高", always: "总是" };

// participationLevelNote 给出档位对应的分数门槛说明，例如「≥0.50」。
// compact 用于一行摘要，省略最严/最松的补充。
export function participationLevelNote(level: string, compact = false): string {
  if (level === "off") return "不判断";
  if (level === "always") return "不看分数";
  const threshold = participationLevelThresholds[level];
  if (threshold == null) return "";
  const text = `≥${threshold.toFixed(2)}`;
  if (compact) return text;
  if (level === "minimal") return `${text}，最严`;
  if (level === "extreme") return `${text}，最松`;
  return text;
}

// participationLevelLabel 把档位渲染成带门槛的文案，例如「中（≥0.50）」「极低（≥0.90，最严）」。
// label 用于覆盖默认的档位名称，compact 输出摘要用的「中 ≥0.50」。
export function participationLevelLabel(level: string, options: { label?: string; compact?: boolean } = {}): string {
  const name = options.label ?? participationLevelNames[level] ?? level;
  const note = participationLevelNote(level, options.compact);
  if (!note) return name;
  return options.compact ? `${name} ${note}` : `${name}（${note}）`;
}

export const participationThresholdOptions = [
  { value: "low", label: "低", score: 40, hint: "较宽松，40 分起" },
  { value: "medium", label: "中", score: 60, hint: "默认，60 分起" },
  { value: "high", label: "高", score: 80, hint: "较严格，80 分起" },
  { value: "max", label: "极高", score: 90, hint: "很严格，90 分起" },
];

export function participationThresholdLevel(value = defaultParticipationScoreThreshold): string {
  return participationThresholdOptions.find(option => option.score === value)?.value ?? "custom";
}

export function changeParticipationThresholdLevel(current: ParticipationPreferences, key: "relevance_threshold" | "substance_threshold", level: string): ParticipationPreferences {
  const option = participationThresholdOptions.find(option => option.value === level);
  return option ? changeParticipationThreshold(current, key, option.score) : current;
}

export function changeParticipationLevel(current: ParticipationPreferences, level: string): ParticipationPreferences {
  return { ...current, ...participationPreset(level, current.cooldown_seconds ?? defaultParticipationCooldownSeconds) };
}

export function changeParticipationThreshold(current: ParticipationPreferences, key: "relevance_threshold" | "substance_threshold", value: number): ParticipationPreferences {
  if (!Number.isFinite(value)) return current;
  return { ...current, [key]: Math.max(0, Math.min(100, Math.round(value))) };
}

export const participationLevelOptions = [
  { value: "low", label: "低", hint: "偶尔补充有用信息，尽量不打扰。" },
  { value: "medium", label: "中", hint: "自然参与，适当接话，聊完收住。" },
  { value: "high", label: "高", hint: "更主动接话，参与闲聊和后续讨论。" },
  { value: "max", label: "极高", hint: "积极参与分享和玩梗，也更愿意继续聊。" },
];

export const participationPresets = [
  { value: "off", label: "关闭", score: 0 },
  { value: "low", label: "低", score: 25 },
  { value: "medium", label: "中", score: 50 },
  { value: "high", label: "高", score: 75 },
  { value: "max", label: "极高", score: 100 },
];

export function participationPreset(level: string, cooldownSeconds = defaultParticipationCooldownSeconds): ParticipationPreferences {
  const score = participationPresets.find(p => p.value === level)?.score ?? 25;
  return { desire: score, social: score, followup: score, restraint: 100 - score, information: 100 - score, cooldown_seconds: cooldownSeconds };
}

export function participationPresetName(value: ParticipationPreferences): string {
  if (value.desire <= 0) return "off";
  if (value.desire <= 37) return "low";
  if (value.desire <= 62) return "medium";
  if (value.desire <= 87) return "high";
  return "max";
}

export function participationFromConfig(config: { participation?: ParticipationPreferences; chat_in_cooldown_seconds?: number; chat_in_level?: string; chat_in_enabled?: boolean; natural_interjection_enabled?: boolean; response_mode?: string }): ParticipationPreferences {
  if (config.participation) return { ...config.participation, cooldown_seconds: config.participation.cooldown_seconds ?? defaultParticipationCooldownSeconds };
  const cooldown = config.chat_in_cooldown_seconds && config.chat_in_cooldown_seconds > 0 ? config.chat_in_cooldown_seconds : defaultParticipationCooldownSeconds;
  if (config.chat_in_enabled === false || config.chat_in_level === "off") return participationPreset("off", cooldown);
  const legacy = ({ quiet: "off", assistant: "low", standard: "low", active: "high", super_active: "max" } as Record<string,string>)[config.response_mode ?? ""];
  return participationPreset(config.natural_interjection_enabled ? "max" : config.chat_in_level || legacy || "low", cooldown);
}
