export interface ParticipationPreferences {
  desire: number;
  social: number;
  followup: number;
  restraint: number;
  information: number;
  cooldown_seconds?: number;
}

export const participationPresets = [
  { value: "off", label: "关闭", score: 0 },
  { value: "low", label: "低", score: 25 },
  { value: "medium", label: "中", score: 50 },
  { value: "high", label: "高", score: 75 },
  { value: "max", label: "极高", score: 100 },
];

export function participationPreset(level: string): ParticipationPreferences {
  const score = participationPresets.find(p => p.value === level)?.score ?? 25;
  const cooldown = ({ off: 0, low: 600, medium: 300, high: 120, max: 30 } as Record<string,number>)[level] ?? 600;
  return { desire: score, social: score, followup: score, restraint: 100 - score, information: 100 - score, cooldown_seconds: cooldown };
}

export function participationPresetName(value: ParticipationPreferences): string {
  return participationPresets.find(p => {
    const preset = participationPreset(p.value);
    return (Object.keys(preset) as (keyof ParticipationPreferences)[]).every(key => preset[key] === (value[key] ?? 0));
  })?.value ?? "custom";
}

export function participationFromConfig(config: { participation?: ParticipationPreferences; chat_in_cooldown_seconds?: number; chat_in_level?: string; chat_in_enabled?: boolean; natural_interjection_enabled?: boolean; response_mode?: string }): ParticipationPreferences {
  if (config.participation) return { ...config.participation };
  if (config.chat_in_enabled === false || config.chat_in_level === "off") return participationPreset("off");
  const legacy = ({ quiet: "off", assistant: "low", standard: "low", active: "high", super_active: "max" } as Record<string,string>)[config.response_mode ?? ""];
  return { ...participationPreset(config.natural_interjection_enabled ? "max" : config.chat_in_level || legacy || "low"), cooldown_seconds: config.chat_in_cooldown_seconds ?? 0 };
}
