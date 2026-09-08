<script setup lang="ts">
import { computed, ref } from "vue";
import { SlidersHorizontal } from "@lucide/vue";
import AppSelect from "./AppSelect.vue";
import { participationPreset, participationPresetName, participationPresets, type ParticipationPreferences } from "../participation";

const props = defineProps<{ modelValue?: ParticipationPreferences; level?: string; inheritable?: boolean; inheritedValue?: ParticipationPreferences }>();
const emit = defineEmits<{ "update:modelValue": [value: ParticipationPreferences | undefined] }>();
const value = computed(() => props.modelValue ?? (props.level ? participationPreset(props.level) : props.inheritedValue ?? participationPreset("low")));
const inherited = computed(() => props.inheritable && !props.modelValue && !props.level);
const custom = ref(false);
const expanded = ref(false);
const selected = computed(() => inherited.value ? "inherit" : custom.value ? "custom" : participationPresetName(value.value));
const options = computed(() => [
  ...(props.inheritable ? [{ value: "inherit", label: "跟随机器人" }] : []),
  ...participationPresets,
  { value: "custom", label: "自定义" },
]);
const fields: { key: keyof ParticipationPreferences; label: string; low: string; high: string }[] = [
  { key: "desire", label: "主动参与", low: "不主动插话", high: "积极参与" },
  { key: "social", label: "闲聊接话", low: "偏好明确请求", high: "愿意闲聊共鸣" },
  { key: "followup", label: "连续跟进", low: "答完退出", high: "跟进后续交流" },
  { key: "restraint", label: "介入克制", low: "自由接话", high: "尽量不打扰" },
  { key: "information", label: "新增信息要求", low: "允许闲聊共鸣", high: "偏好新信息" },
];
function select(name: string) {
  custom.value = name === "custom";
  if (name === "custom") expanded.value = true;
  emit("update:modelValue", name === "inherit" ? undefined : name === "custom" ? { ...value.value } : participationPreset(name));
}
function update(key: keyof ParticipationPreferences, event: Event) {
  const input = event.target as HTMLInputElement;
  if (input.value === "" || !Number.isFinite(input.valueAsNumber)) return;
  const next = Math.max(0, Math.min(key === "cooldown_seconds" ? 3600 : 100, Math.round(input.valueAsNumber)));
  input.value = String(next);
  custom.value = true;
  emit("update:modelValue", { ...value.value, [key]: next });
}
function restoreNumber(key: keyof ParticipationPreferences, event: Event) {
  (event.target as HTMLInputElement).value = String(value.value[key] ?? 0);
}
</script>

<template>
  <div class="participation-controls">
    <div class="preferences-toolbar">
      <AppSelect aria-label="发言偏好预设" :model-value="selected" :options="options" @update:model-value="select" />
      <button v-if="!inherited" type="button" class="btn preferences-toggle" :class="{ active: expanded }" aria-label="详细发言偏好" :title="expanded ? '收起详细发言偏好' : '展开详细发言偏好'" :aria-expanded="expanded" @click="expanded = !expanded">
        <SlidersHorizontal :size="16" aria-hidden="true" />
      </button>
    </div>
    <template v-if="!inherited">
      <div v-if="expanded" class="preferences-details">
        <div v-for="field in fields" :key="field.key" class="preference-row">
          <div class="preference-label" :title="`0：${field.low}；100：${field.high}`">
            <span>{{ field.label }}</span>
            <small>{{ value[field.key] === 0 ? field.low : value[field.key] === 100 ? field.high : (value[field.key] ?? 0) < 50 ? '较低' : (value[field.key] ?? 0) > 50 ? '较高' : '适中' }}</small>
          </div>
          <input class="preference-range" type="range" min="0" max="100" step="1" :aria-label="field.label" :title="`0：${field.low}；100：${field.high}`" :style="{ '--range-value': `${value[field.key] ?? 0}%` }" :value="value[field.key]" @input="update(field.key, $event)" />
          <input class="preference-number" type="number" inputmode="numeric" min="0" max="100" step="1" :aria-label="`${field.label}数值`" :value="value[field.key]" @input="update(field.key, $event)" @blur="restoreNumber(field.key, $event)" />
        </div>
      </div>
      <div class="cooldown-row">
        <div class="preference-label"><span>主动闲聊冷却</span><small>{{ value.cooldown_seconds ? '仅限制主动接话' : '不限制间隔' }}</small></div>
        <span class="number-unit"><input class="preference-number cooldown-number" type="number" inputmode="numeric" min="0" max="3600" step="1" aria-label="主动闲聊冷却秒数" :value="value.cooldown_seconds ?? 0" @input="update('cooldown_seconds', $event)" @blur="restoreNumber('cooldown_seconds', $event)" /><span aria-hidden="true">秒</span></span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.participation-controls { display: grid; gap: 8px; min-width: 0; }
.preferences-toolbar { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px; }
.preferences-toggle { width: 36px; height: 36px; padding: 0; display: grid; place-items: center; border-radius: 6px; }
.preferences-toggle.active { color: var(--accent); border-color: var(--accent); background: var(--accent-soft); }
.preference-row { display: grid; grid-template-columns: 104px minmax(40px, 1fr) 58px; align-items: center; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--border); }
.preference-label { display: grid; gap: 2px; min-width: 0; font-size: 13px; line-height: 20px; }
.preference-label small { color: var(--muted); font-size: 11px; line-height: 16px; }
.preference-number { box-sizing: border-box; width: 58px; min-width: 0; height: 36px; padding: 4px 6px; border: 1px solid transparent; border-bottom-color: var(--border); border-radius: 4px 4px 0 0; background: transparent; color: var(--text); text-align: center; font-family: inherit; font-size: 13px; font-weight: 500; font-variant-numeric: tabular-nums; appearance: textfield; transition: background .15s, border-color .15s; }
.preference-number::-webkit-inner-spin-button, .preference-number::-webkit-outer-spin-button { appearance: none; margin: 0; }
.preference-number:hover { background: var(--accent-soft); border-bottom-color: var(--accent); }
.preference-number:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; background: var(--accent-soft); border-radius: 4px; }
.preference-range { appearance: none; width: 100%; min-width: 0; height: 32px; margin: 0; cursor: pointer; background: linear-gradient(to right, var(--accent) var(--range-value), var(--border) var(--range-value)) center / 100% 4px no-repeat; accent-color: var(--accent); }
.preference-range::-webkit-slider-runnable-track { height: 4px; background: transparent; }
.preference-range::-webkit-slider-thumb { appearance: none; width: 14px; height: 14px; margin-top: -5px; border-radius: 50%; border: 2px solid var(--accent); background: var(--accent); }
.preference-range::-moz-range-track { height: 4px; background: transparent; }
.preference-range::-moz-range-thumb { width: 10px; height: 10px; border-radius: 50%; border: 2px solid var(--accent); background: var(--accent); }
.cooldown-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-top: 8px; }
.number-unit { display: flex; align-items: center; gap: 2px; padding-right: 8px; border: 1px solid transparent; border-bottom-color: var(--border); border-radius: 4px 4px 0 0; color: var(--muted); font-size: 12px; }
.number-unit:hover { background: var(--accent-soft); border-bottom-color: var(--accent); }
.number-unit:focus-within { outline: 2px solid var(--accent); outline-offset: 1px; background: var(--accent-soft); border-radius: 4px; }
.cooldown-number { width: 64px; text-align: right; border: 0; }
.cooldown-number:hover, .cooldown-number:focus-visible { outline: none; background: transparent; }
@media (max-width: 420px) { .preference-row { grid-template-columns: 92px minmax(40px, 1fr) 54px; gap: 8px; } .preference-number { width: 54px; } .cooldown-number { width: 64px; } }
</style>
