<script setup lang="ts">
import { computed } from "vue";
import AppSelect from "./AppSelect.vue";
import { defaultParticipationCooldownSeconds, participationPreset, participationPresetName, type ParticipationPreferences } from "../participation";

const props = defineProps<{ modelValue?: ParticipationPreferences; level?: string; inheritable?: boolean; inheritedValue?: ParticipationPreferences }>();
const emit = defineEmits<{ "update:modelValue": [value: ParticipationPreferences | undefined] }>();
const value = computed(() => props.modelValue ?? (props.level ? participationPreset(props.level) : props.inheritedValue ?? participationPreset("low")));
const inherited = computed(() => props.inheritable && !props.modelValue && !props.level);
const cooldownSeconds = computed(() => value.value.cooldown_seconds ?? defaultParticipationCooldownSeconds);
const preset = computed(() => participationPresetName(value.value));
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
const ratingOptions = [{ value: "off", label: "关" }, { value: "minimal", label: "极低", hint: "0.90 起" }, { value: "low", label: "低", hint: "0.70 起" }, { value: "medium", label: "中", hint: "0.50 起" }, { value: "high", label: "高", hint: "0.30 起" }, { value: "extreme", label: "极高", hint: "0.10 起" }, { value: "always", label: "总是" }];
function ratingLevel(key: "relevance_level" | "chat_level" | "answerability_level") { return value.value[key] ?? (key === "answerability_level" ? "medium" : preset.value === "off" ? "off" : key === "relevance_level" ? "medium" : preset.value === "max" ? "always" : preset.value); }
function setRatingLevel(key: "relevance_level" | "chat_level" | "answerability_level", level: string) { emit("update:modelValue", { ...value.value, [key]: level }); }
</script>

<template>
  <div class="participation-controls">
    <AppSelect v-if="inheritable" aria-label="配置来源" :model-value="inherited ? 'inherit' : 'custom'" :options="[{value:'inherit',label:'跟随机器人'},{value:'custom',label:'本群设置'}]" @update:model-value="emit('update:modelValue', $event === 'inherit' ? undefined : {...value})" />
    <template v-if="!inherited">
      <div class="cooldown-row"><span>相关度</span><AppSelect aria-label="相关度等级" :model-value="ratingLevel('relevance_level')" :options="ratingOptions" @update:model-value="setRatingLevel('relevance_level', $event)" /></div>
      <div class="cooldown-row"><span>闲聊等级</span><AppSelect aria-label="闲聊等级" :model-value="ratingLevel('chat_level')" :options="ratingOptions" @update:model-value="setRatingLevel('chat_level', $event)" /></div>
      <div class="cooldown-row">
        <div class="cooldown-label"><span>主动闲聊冷却</span><small>独立设置，默认 30 秒</small></div>
        <span class="number-unit"><input class="cooldown-number" type="number" inputmode="numeric" min="0" max="3600" step="1" aria-label="主动闲聊冷却秒数" :value="cooldownSeconds" @input="updateCooldown" @blur="restoreCooldown" /><span aria-hidden="true">秒</span></span>
      </div>
      <span class="hint">成功发送主动闲聊后开始计时，0 表示不限制间隔；已进入直接回复流程的请求不受影响。</span>
      <div class="cooldown-row"><span>可回答门槛</span><AppSelect aria-label="可回答门槛" :model-value="ratingLevel('answerability_level')" :options="ratingOptions" @update:model-value="setRatingLevel('answerability_level', $event)" /></div>
      <p class="hint">回复条件：（相关度达标，或闲聊分达标且冷却结束），并且可回答分达标。</p>
      <p class="hint">相关度衡量是否明确向机器人提问或追问；闲聊分衡量普通群聊是否适合插话；可回答分衡量预计回复是否有意义，低质量回答不应回复。需要搜索或调用工具不代表不可回答。</p>
      <p class="hint">相关度达标不受闲聊冷却限制，但仍须通过可回答门槛。相关度、闲聊选“关”表示关闭该分支；可回答门槛选“关”表示不检查质量分。“总是”跳过该项分数门槛，停止请求和重复循环仍保持沉默。</p>
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
