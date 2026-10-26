export interface RandomRepeatConfig {
  enabled: boolean;
  threshold: number;
  probability: number;
  cooldown_seconds: number;
}

export function randomRepeatDefaults(value?: Partial<RandomRepeatConfig>): RandomRepeatConfig {
  return { enabled: value?.enabled ?? false, threshold: value?.threshold ?? 2, probability: value?.probability ?? 30, cooldown_seconds: value?.cooldown_seconds ?? -1 };
}
