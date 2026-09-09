<script setup lang="ts">
import { computed } from "vue";
import AppSelect from "./AppSelect.vue";
import { defaultParticipationCooldownSeconds, defaultParticipationScoreThreshold, changeParticipationLevel, changeParticipationThresholdLevel, participationThresholdLevel, participationThresholdOptions, participationPreset, participationPresetName, participationLevelOptions, type ParticipationPreferences } from "../participation";

const props = defineProps<{ modelValue?: ParticipationPreferences; level?: string; inheritable?: boolean; inheritedValue?: ParticipationPreferences }>();
const emit = defineEmits<{ "update:modelValue": [value: ParticipationPreferences | undefined] }>();
const value = computed(() => props.modelValue ?? (props.level ? participationPreset(props.level) : props.inheritedValue ?? participationPreset("low")));
const inherited = computed(() => props.inheritable && !props.modelValue && !props.level);
const cooldownSeconds = computed(() => value.value.cooldown_seconds ?? defaultParticipationCooldownSeconds);
const preset = computed(() => participationPresetName(value.value));
const selected = computed(() => inherited.value ? "inherit" : preset.value === "off" ? "已关闭（原有配置）" : preset.value);
const thresholds = [
  { key: "relevance_threshold" as const, label: "与机器人相关度门槛", description: "用于判断是否在回应机器人" },
  { key: "substance_threshold" as const, label: "闲聊实质性门槛", description: "越高越克制，不影响明确提问" },
];
const options = computed(() => [
  ...(props.inheritable ? [{ value: "inherit", label: "跟随机器人" }] : []),
  ...participationLevelOptions,
]);
const description = computed(() => {
  if (inherited.value) return "使用所属机器人的回复欲望、评分门槛和冷却设置。";
  if (preset.value === "off") return "当前不主动插话；选择档位后恢复参与。";
  return participationLevelOptions.find(option => option.value === preset.value)?.hint ?? "保留原有发言偏好；选择档位后应用对应预设。";
});
function select(name: string) {
  if (name === "inherit" && props.inheritable) {
    emit("update:modelValue", undefined);
  } else if (participationLevelOptions.some(option => option.value === name)) {
    emit("update:modelValue", changeParticipationLevel(value.value, name));
  }
}
function updateCooldown(event: Event) {
  const input = event.target as HTMLInputElement;
  if (input.value === "" || !Number.isFinite(input.valueAsNumber)) return;
  const next = Math.max(0, Math.min(3600, Math.round(input.valueAsNumber)));
  input.value = String(next);
  emit("update:modelValue", { ...value.value, cooldown_seconds: next });
}
function restoreCooldown(event: Event) {
  (event.target as HTMLInputElement).value = String(cooldownSeconds.value);
}
function thresholdValue(key: "relevance_threshold" | "substance_threshold") {
  return value.value[key] ?? defaultParticipationScoreThreshold;
}
function thresholdOptions(key: "relevance_threshold" | "substance_threshold") {
  return participationThresholdLevel(thresholdValue(key)) === "custom"
    ? [...participationThresholdOptions, { value: "custom", label: `自定义（${thresholdValue(key)} 分）`, score: thresholdValue(key), hint: "保留已有配置" }]
    : participationThresholdOptions;
}
function selectThreshold(key: "relevance_threshold" | "substance_threshold", level: string) {
  emit("update:modelValue", changeParticipationThresholdLevel(value.value, key, level));
}
</script>

<template>
  <div class="participation-controls">
    <AppSelect aria-label="回复欲望" :model-value="selected" :options="options" @update:model-value="select" />
    <span class="hint">{{ description }}</span>
    <span v-if="!inherited" class="hint">切换档位不会修改评分门槛和冷却时间。</span>
    <template v-if="!inherited">
      <div v-for="threshold in thresholds" :key="threshold.key" class="cooldown-row">
        <div class="cooldown-label"><span>{{ threshold.label }}</span><small>{{ threshold.description }}</small></div>
        <div class="threshold-select"><AppSelect :aria-label="threshold.label" :model-value="participationThresholdLevel(thresholdValue(threshold.key))" :options="thresholdOptions(threshold.key)" @update:model-value="selectThreshold(threshold.key, $event)" /></div>
      </div>
      <span class="hint">门槛越高越严格；两项分别判断，默认“中”，不取平均。</span>
      <div class="cooldown-row">
        <div class="cooldown-label"><span>主动闲聊冷却</span><small>独立设置，默认 30 秒</small></div>
        <span class="number-unit"><input class="cooldown-number" type="number" inputmode="numeric" min="0" max="3600" step="1" aria-label="主动闲聊冷却秒数" :value="cooldownSeconds" @input="updateCooldown" @blur="restoreCooldown" /><span aria-hidden="true">秒</span></span>
      </div>
      <span class="hint">成功发送主动闲聊后开始计时，0 表示不限制间隔；公开提问和点名请求不受影响。</span>
    </template>
  </div>
</template>

<style scoped>
.participation-controls { display: grid; gap: 8px; min-width: 0; }
.cooldown-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 8px; padding-top: 16px; border-top: 1px solid var(--border); }
.cooldown-label { display: grid; gap: 2px; min-width: 0; font-size: 13px; line-height: 20px; }
.cooldown-label small { color: var(--muted); font-size: 11px; line-height: 16px; }
.threshold-select { width: 150px; max-width: 50%; flex-shrink: 0; }
.number-unit { display: flex; align-items: center; gap: 2px; padding-right: 8px; border: 1px solid transparent; border-bottom-color: var(--border); border-radius: 4px 4px 0 0; color: var(--muted); font-size: 12px; }
.number-unit:hover { background: var(--accent-soft); border-bottom-color: var(--accent); }
.number-unit:focus-within { outline: 2px solid var(--accent); outline-offset: 1px; background: var(--accent-soft); border-radius: 4px; }
.cooldown-number { box-sizing: border-box; width: 64px; min-width: 0; height: 36px; padding: 4px 6px; border: 0; background: transparent; color: var(--text); text-align: right; font-family: inherit; font-size: 13px; font-weight: 500; font-variant-numeric: tabular-nums; appearance: textfield; }
.cooldown-number::-webkit-inner-spin-button, .cooldown-number::-webkit-outer-spin-button { appearance: none; margin: 0; }
.cooldown-number:focus-visible { outline: none; }
</style>
