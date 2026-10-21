<script setup lang="ts">
import { computed, useId } from "vue";
import AppSelect from "./AppSelect.vue";
import { randomRepeatDefaults, type RandomRepeatConfig } from "../lib/random-repeat";

const props = defineProps<{ modelValue?: RandomRepeatConfig; inheritable?: boolean; inheritedValue?: RandomRepeatConfig }>();
const emit = defineEmits<{ "update:modelValue": [value: RandomRepeatConfig | undefined] }>();
const id = useId();
const value = computed(() => randomRepeatDefaults(props.modelValue ?? props.inheritedValue));
const mode = computed(() => props.inheritable && !props.modelValue ? "inherit" : value.value.enabled ? "on" : "off");
function setMode(mode: string) {
  emit("update:modelValue", mode === "inherit" ? undefined : { ...value.value, enabled: mode === "on" });
}
function update(key: "threshold" | "probability" | "cooldown_seconds", event: Event) {
  emit("update:modelValue", { ...value.value, [key]: Number((event.target as HTMLInputElement).value) });
}
</script>

<template>
  <div class="form-grid">
    <div class="field wide">
      <template v-if="inheritable">
        <label :for="`${id}-mode`">本群随机复读</label>
        <AppSelect :id="`${id}-mode`" :model-value="mode" :options="[{ value: 'inherit', label: `跟随机器人（${inheritedValue?.enabled ? '开启' : '关闭'}）` }, { value: 'on', label: '本群开启' }, { value: 'off', label: '本群关闭' }]" @update:model-value="setMode" />
      </template>
      <label v-else class="switch">
        <input type="checkbox" :checked="value.enabled" @change="setMode(($event.target as HTMLInputElement).checked ? 'on' : 'off')" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">随机复读</span>
      </label>
      <span class="hint">不同群友连续发送相同纯文字时，随机原样跟一句。达到参与人数后，每条新复读都按概率判断；成功复读后进入冷却，后续消息不重置冷却，也不因间隔时间重新计数。复读不调用模型，不参与 @、引用、指令、媒体或机器人消息，最多 500 字。</span>
    </div>
    <template v-if="value.enabled && mode !== 'inherit'">
      <div class="field">
        <label :for="`${id}-threshold`">参与人数</label>
        <input :id="`${id}-threshold`" class="input" type="number" min="2" max="10" step="1" :value="value.threshold" @input="update('threshold', $event)" />
      </div>
      <div class="field">
        <label :for="`${id}-probability`">复读概率（%）</label>
        <input :id="`${id}-probability`" class="input" type="number" min="0" max="100" step="1" :value="value.probability" @input="update('probability', $event)" />
      </div>
      <div class="field">
        <label :for="`${id}-cooldown`">冷却时间（秒）</label>
        <input :id="`${id}-cooldown`" class="input" type="number" min="0" max="600" step="1" :value="value.cooldown_seconds" @input="update('cooldown_seconds', $event)" />
      </div>
    </template>
  </div>
</template>
