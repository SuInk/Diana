<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div v-if="settings.length > 0 || hasSecrets" class="group-plugin-settings">
    <button class="group-plugin-settings-toggle" type="button" :aria-expanded="expanded" @click="expanded = !expanded">
      <SlidersHorizontal :size="14" aria-hidden="true" />
      <span>参数</span>
      <span v-if="overrideCount > 0" class="badge accent">{{ overrideCount }}</span>
      <ChevronDown :size="14" class="group-plugin-settings-chevron" :class="{ open: expanded }" aria-hidden="true" />
    </button>

    <div v-if="expanded" class="group-plugin-settings-panel">
      <div class="group-plugin-setting-list">
        <div v-for="spec in settings" :key="spec.key" class="group-plugin-setting">
          <div class="group-plugin-setting-head">
            <div>
              <label :for="controlID(spec)">{{ spec.label }}</label>
              <span v-if="spec.description" class="hint">{{ spec.description }}</span>
            </div>
            <div class="segmented group-plugin-setting-mode">
              <button type="button" :class="{ active: !isCustom(spec.key) }" @click="setCustom(spec, false)">跟随</button>
              <button type="button" :class="{ active: isCustom(spec.key) }" @click="setCustom(spec, true)">自定义</button>
            </div>
          </div>

          <div v-if="isCustom(spec.key)" class="group-plugin-setting-control">
            <label v-if="spec.type === 'bool'" class="switch">
              <input
                :id="controlID(spec)"
                type="checkbox"
                :checked="Boolean(settingValue(spec))"
                @change="setValue(spec.key, ($event.target as HTMLInputElement).checked)"
              />
              <span class="track" aria-hidden="true"></span>
            </label>
            <div v-else-if="spec.type === 'number'" class="plugin-setting-number">
              <input
                :id="controlID(spec)"
                class="input"
                type="number"
                :value="settingValue(spec)"
                :min="spec.min"
                :max="spec.max"
                :step="spec.step || 1"
                @input="setNumberValue(spec.key, $event)"
              />
              <span v-if="spec.unit" class="plugin-setting-unit">{{ spec.unit }}</span>
            </div>
            <AppSelect
              v-else-if="spec.type === 'select'"
              :id="controlID(spec)"
              :model-value="String(settingValue(spec) ?? '')"
              :options="spec.options ?? []"
              @update:model-value="setValue(spec.key, $event)"
            />
            <div v-else-if="spec.type === 'multi_select'" :id="controlID(spec)" class="plugin-setting-checks">
              <label v-for="option in spec.options ?? []" :key="option.value" class="check-item">
                <input
                  type="checkbox"
                  :checked="multiSelected(spec, option.value)"
                  @change="toggleMulti(spec, option.value, $event)"
                />
                <span>{{ option.label }}</span>
              </label>
            </div>
            <input
              v-else
              :id="controlID(spec)"
              class="input"
              type="text"
              :value="String(settingValue(spec) ?? '')"
              @input="setValue(spec.key, ($event.target as HTMLInputElement).value)"
            />
          </div>
          <span v-else class="group-plugin-setting-inherited">全局：{{ displayValue(spec) }}</span>
        </div>
      </div>
      <p v-if="hasSecrets" class="group-plugin-secret-note">凭据类参数沿用全局插件设置。</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { ChevronDown, SlidersHorizontal } from "@lucide/vue";
import type { PluginSettingSpec, PluginState } from "../api";
import AppSelect from "./AppSelect.vue";

const props = defineProps<{
  plugin: PluginState;
  modelValue?: Record<string, unknown>;
}>();
const emit = defineEmits<{ "update:modelValue": [Record<string, unknown>] }>();

const settings = computed(() => (props.plugin.manifest.settings ?? []).filter((spec) => !spec.secret));
const hasSecrets = computed(() => (props.plugin.manifest.settings ?? []).some((spec) => spec.secret));
const overrideCount = computed(() => Object.keys(props.modelValue ?? {}).length);
const expanded = ref(overrideCount.value > 0);

function isCustom(key: string): boolean {
  return Object.prototype.hasOwnProperty.call(props.modelValue ?? {}, key);
}

function globalValue(spec: PluginSettingSpec): unknown {
  if (Object.prototype.hasOwnProperty.call(props.plugin.settings ?? {}, spec.key)) {
    return props.plugin.settings?.[spec.key];
  }
  return spec.default;
}

function settingValue(spec: PluginSettingSpec): unknown {
  return isCustom(spec.key) ? props.modelValue?.[spec.key] : globalValue(spec);
}

function setCustom(spec: PluginSettingSpec, custom: boolean): void {
  const next = { ...(props.modelValue ?? {}) };
  if (!custom) {
    delete next[spec.key];
  } else if (!Object.prototype.hasOwnProperty.call(next, spec.key)) {
    const value = globalValue(spec);
    next[spec.key] = Array.isArray(value) ? [...value] : value;
  }
  emit("update:modelValue", next);
}

function setValue(key: string, value: unknown): void {
  emit("update:modelValue", { ...(props.modelValue ?? {}), [key]: value });
}

function setNumberValue(key: string, event: Event): void {
  const raw = (event.target as HTMLInputElement).value;
  if (raw !== "" && Number.isFinite(Number(raw))) {
    setValue(key, Number(raw));
  }
}

function multiSelected(spec: PluginSettingSpec, option: string): boolean {
  const value = settingValue(spec);
  return Array.isArray(value) && value.includes(option);
}

function toggleMulti(spec: PluginSettingSpec, option: string, event: Event): void {
  const checked = (event.target as HTMLInputElement).checked;
  const value = settingValue(spec);
  const next = Array.isArray(value) ? [...value] : [];
  const index = next.indexOf(option);
  if (checked && index < 0) {
    next.push(option);
  } else if (!checked && index >= 0) {
    next.splice(index, 1);
  }
  setValue(spec.key, next);
}

function controlID(spec: PluginSettingSpec): string {
  return `group-plugin-${props.plugin.manifest.id}-${spec.key}`.replace(/[^a-zA-Z0-9_-]/g, "-");
}

function displayValue(spec: PluginSettingSpec): string {
  const value = globalValue(spec);
  if (spec.type === "bool") {
    return value ? "开启" : "关闭";
  }
  if (spec.type === "multi_select") {
    const selected = Array.isArray(value) ? value : [];
    const labels = (spec.options ?? []).filter((option) => selected.includes(option.value)).map((option) => option.label);
    return labels.join("、") || "无";
  }
  if (spec.type === "select") {
    return spec.options?.find((option) => option.value === value)?.label ?? String(value ?? "");
  }
  const text = String(value ?? "");
  return text ? `${text}${spec.unit ? ` ${spec.unit}` : ""}` : "空";
}
</script>
