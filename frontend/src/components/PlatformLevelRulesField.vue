<template>
  <section class="platform-rules">
    <div class="platform-rules-head">
      <div>
        <label>{{ spec.label }}</label>
        <p v-if="spec.description" class="hint">{{ spec.description }}</p>
      </div>
      <button class="btn small" type="button" @click="addRule">
        <Plus :size="14" aria-hidden="true" />
        新增规则
      </button>
    </div>

    <div v-if="rules.length" class="platform-rule-list">
      <article v-for="(rule, index) in rules" :key="index" class="platform-rule-row">
        <div class="platform-rule-main">
          <div class="field">
            <label :for="`${spec.key}-platform-${index}`">平台</label>
            <AppSelect
              :id="`${spec.key}-platform-${index}`"
              :model-value="rule.platform"
              :options="spec.options ?? []"
              @update:model-value="updateRule(index, { platform: String($event) })"
            />
          </div>
          <div class="field">
            <label :for="`${spec.key}-level-${index}`">最低群等级</label>
            <input :id="`${spec.key}-level-${index}`" :value="rule.minimum_level" class="input" type="number" min="0" max="1000" step="1" @input="updateLevel(index, $event)" />
          </div>
          <div class="field">
            <label :for="`${spec.key}-unknown-${index}`">等级未知</label>
            <AppSelect
              :id="`${spec.key}-unknown-${index}`"
              :model-value="rule.unknown_policy"
              :options="unknownOptions"
              @update:model-value="updateRule(index, { unknown_policy: String($event) as 'allow' | 'deny' })"
            />
          </div>
          <button class="btn ghost icon-only danger platform-rule-delete" type="button" title="删除规则" aria-label="删除规则" @click="removeRule(index)">
            <Trash2 :size="15" aria-hidden="true" />
          </button>
        </div>
        <div class="platform-rule-switches">
          <label><input :checked="rule.enabled" type="checkbox" @change="updateBoolean(index, 'enabled', $event)" />启用</label>
          <label><input :checked="rule.owner_bypass" type="checkbox" @change="updateBoolean(index, 'owner_bypass', $event)" />主人豁免</label>
          <label><input :checked="rule.mention_bypass" type="checkbox" @change="updateBoolean(index, 'mention_bypass', $event)" />@机器人时绕过</label>
        </div>
      </article>
    </div>
    <p v-else class="platform-rules-empty">没有规则，所有平台均不受链接解析等级限制。</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import type { PluginSettingSpec } from "../api";
import AppSelect from "./AppSelect.vue";

interface PlatformLevelRule {
  platform: string;
  minimum_level: number;
  unknown_policy: "allow" | "deny";
  owner_bypass: boolean;
  mention_bypass: boolean;
  enabled: boolean;
}

const props = defineProps<{ spec: PluginSettingSpec; modelValue?: unknown }>();
const emit = defineEmits<{ "update:modelValue": [PlatformLevelRule[]] }>();
const unknownOptions = [
  { value: "deny", label: "拦截" },
  { value: "allow", label: "放行" }
];

const rules = computed<PlatformLevelRule[]>(() => {
  return Array.isArray(props.modelValue) ? props.modelValue as PlatformLevelRule[] : [];
});

function addRule(): void {
  emit("update:modelValue", [...rules.value, {
    platform: props.spec.options?.[0]?.value ?? "x",
    minimum_level: 0,
    unknown_policy: "deny",
    owner_bypass: true,
    mention_bypass: false,
    enabled: true
  }]);
}

function removeRule(index: number): void {
  emit("update:modelValue", rules.value.filter((_, itemIndex) => itemIndex !== index));
}

function updateRule(index: number, patch: Partial<PlatformLevelRule>): void {
  emit("update:modelValue", rules.value.map((rule, itemIndex) => itemIndex === index ? { ...rule, ...patch } : rule));
}

function updateLevel(index: number, event: Event): void {
  const value = Number((event.target as HTMLInputElement).value);
  if (Number.isFinite(value)) updateRule(index, { minimum_level: value });
}

function updateBoolean(index: number, key: "enabled" | "owner_bypass" | "mention_bypass", event: Event): void {
  updateRule(index, { [key]: (event.target as HTMLInputElement).checked });
}
</script>

<style scoped>
.platform-rules { display: grid; gap: 12px; }
.platform-rules-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.platform-rules-head label { font-weight: 650; }
.platform-rules-head .hint { margin: 4px 0 0; }
.platform-rule-list { display: grid; gap: 10px; }
.platform-rule-row { border: 1px solid var(--border); border-radius: var(--radius-md); padding: 12px; display: grid; gap: 10px; }
.platform-rule-main { display: grid; grid-template-columns: minmax(140px, 1fr) minmax(120px, .7fr) minmax(120px, .8fr) 34px; gap: 10px; align-items: end; }
.platform-rule-delete { width: 34px; height: 34px; }
.platform-rule-switches { display: flex; flex-wrap: wrap; gap: 16px; }
.platform-rule-switches label { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 13px; }
.platform-rules-empty { margin: 0; color: var(--text-muted); font-size: 13px; }
@media (max-width: 720px) {
  .platform-rules-head { align-items: stretch; flex-direction: column; }
  .platform-rule-main { grid-template-columns: 1fr 1fr; }
  .platform-rule-delete { justify-self: end; }
}
</style>
