<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->
<template>
  <div class="model-catalog-editor">
    <div class="model-catalog-tools" v-if="!draft">
      <div class="input-group">
        <input v-model="quickIDs" class="input" aria-label="快速添加模型 ID" placeholder="模型 ID，多个用逗号分隔" @keydown.enter.prevent="addIDs" />
        <button class="btn" type="button" :disabled="disabled || !quickIDs.trim()" @click="addIDs">添加 ID</button>
      </div>
      <button class="btn" type="button" :disabled="disabled" @click="start()"><Plus :size="14" aria-hidden="true" />自定义模型</button>
    </div>

    <section v-if="draft" class="model-metadata-panel" aria-label="模型设置">
      <div class="model-metadata-heading">
        <div><strong>{{ editingID ? '编辑模型' : '自定义模型' }}</strong><p class="hint">设置应用到当前草稿，保存提供商配置后生效。</p></div>
        <button class="btn icon-only ghost" type="button" aria-label="取消模型编辑" @click="cancel"><X :size="16" aria-hidden="true" /></button>
      </div>
      <div class="model-metadata-fields">
        <div class="field">
          <label for="model-metadata-id">模型 ID</label>
          <input id="model-metadata-id" ref="idInput" v-model="draft.id" class="input" :readonly="Boolean(editingID)" autocomplete="off" placeholder="例如 my-vision-model" />
        </div>
        <div class="field">
          <label for="model-metadata-context">上下文窗口 <span class="muted">Token</span></label>
          <input id="model-metadata-context" ref="contextInput" v-model="draft.context" class="input" inputmode="numeric" placeholder="留空使用提供商设置或兜底值" aria-describedby="model-context-hint" />
          <span id="model-context-hint" class="hint">{{ contextHint }}</span>
        </div>
      </div>
      <AppSwitch v-model="draft.overrideCapabilities">手动设置模型能力</AppSwitch>
      <p class="hint">{{ draft.overrideCapabilities ? '输入图片 + 输出文字用于视觉理解；输出图片用于图片生成。' : '跟随同步的能力信息；没有能力信息时保持待确认。' }}</p>
      <div v-if="draft.overrideCapabilities" class="model-capability-fields">
        <div class="field">
          <span class="model-capability-label">输入能力</span>
          <div class="model-capability-options" role="group" aria-label="输入能力">
            <button v-for="item in inputOptions" :key="item.value" type="button" class="model-capability-option" :aria-pressed="draft.input.includes(item.value)" @click="toggle('input', item.value)"><Check v-if="draft.input.includes(item.value)" :size="13" aria-hidden="true" />{{ item.label }}</button>
          </div>
        </div>
        <div class="field">
          <span class="model-capability-label">输出能力</span>
          <div class="model-capability-options" role="group" aria-label="输出能力">
            <button v-for="item in outputOptions" :key="item.value" type="button" class="model-capability-option" :aria-pressed="draft.output.includes(item.value)" @click="toggle('output', item.value)"><Check v-if="draft.output.includes(item.value)" :size="13" aria-hidden="true" />{{ item.label }}</button>
          </div>
        </div>
      </div>
      <p v-if="error" class="model-metadata-error" role="alert">{{ error }}</p>
      <div class="model-metadata-footer"><button class="btn ghost" type="button" @click="cancel">取消编辑</button><button class="btn primary" type="button" @click="apply"><Check :size="14" aria-hidden="true" />应用模型设置</button></div>
    </section>

    <div v-if="modelValue.length" class="model-catalog-list" aria-label="模型列表">
      <div v-for="model in modelValue" :key="model.id" class="model-catalog-row" :class="{ selected: editingID === model.id }">
        <button class="model-catalog-details" type="button" :disabled="disabled || Boolean(draft)" :aria-label="`编辑模型 ${model.id}`" @click="start(model)">
          <span class="model-catalog-copy"><span class="model-catalog-id">{{ model.id }}</span><span class="hint">{{ summary(model) }}</span></span><Pencil :size="14" aria-hidden="true" />
        </button>
        <button class="btn icon-only ghost" type="button" :disabled="disabled || Boolean(draft)" :aria-label="`移除模型 ${model.id}`" @click="remove(model.id)"><Trash2 :size="14" aria-hidden="true" /></button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { Check, Pencil, Plus, Trash2, X } from "@lucide/vue";
import type { LLMModelInfo } from "../api";
import { applyModelMetadataDraft, createModelMetadataDraft, modelCapabilitySummary, type ModelMetadataDraft } from "../model-metadata";
import AppSwitch from "./AppSwitch.vue";

const props = defineProps<{ modelValue: LLMModelInfo[]; disabled?: boolean; providerWindow?: number }>();
const emit = defineEmits<{ "update:modelValue": [LLMModelInfo[]]; "editing-change": [boolean] }>();
const quickIDs = ref("");
const draft = ref<ModelMetadataDraft | null>(null);
const editingID = ref<string>();
const error = ref("");
const idInput = ref<HTMLInputElement>();
const contextInput = ref<HTMLInputElement>();
const currentModel = computed(() => props.modelValue.find(model => model.id === editingID.value));
watch(() => props.modelValue, () => {
  if (draft.value && editingID.value && !currentModel.value) cancel();
});
const contextHint = computed(() => {
  const reference = currentModel.value?.context_window_tokens;
  const prefix = props.providerWindow ? `提供商统一覆盖为 ${props.providerWindow.toLocaleString("en-US")}，优先于模型窗口。` : "填写后用于这个模型的实际上下文预算。";
  return prefix + (reference ? `目录参考：${reference.toLocaleString("en-US")} Token。` : "");
});
const modalityOptions = [{ value: "text", label: "文字" }, { value: "image", label: "图片" }, { value: "audio", label: "音频" }, { value: "video", label: "视频" }];
function options(values: string[]) { return [...modalityOptions, ...values.filter(value => !modalityOptions.some(item => item.value === value)).map(value => ({ value, label: value }))]; }
const inputOptions = computed(() => options(draft.value?.input ?? []));
const outputOptions = computed(() => options(draft.value?.output ?? []));

function summary(model: LLMModelInfo): string {
  const parts = [modelCapabilitySummary(model)];
  if (model.context_window_override) parts.push(`${model.context_window_override.toLocaleString("en-US")} Token`);
  if (model.context_window_override || model.capabilities_override) parts.push("手动设置");
  else if (model.custom) parts.push("自定义 ID");
  return parts.join(" · ");
}
async function start(model?: LLMModelInfo) {
  editingID.value = model?.id;
  draft.value = createModelMetadataDraft(model);
  error.value = "";
  emit("editing-change", true);
  await nextTick();
  (model ? contextInput.value : idInput.value)?.focus();
}
function cancel() { draft.value = null; editingID.value = undefined; error.value = ""; emit("editing-change", false); }
function toggle(field: "input" | "output", value: string) {
  if (!draft.value) return;
  const values = draft.value[field];
  draft.value[field] = values.includes(value) ? values.filter(item => item !== value) : [...values, value];
  error.value = "";
}
function apply() {
  if (!draft.value) return;
  try { emit("update:modelValue", applyModelMetadataDraft(draft.value, props.modelValue, editingID.value)); cancel(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "模型设置无效"; }
}
function addIDs() {
  const ids = [...new Set(quickIDs.value.split(/[,，\s]+/).filter(Boolean))];
  emit("update:modelValue", [...props.modelValue, ...ids.filter(id => !props.modelValue.some(model => model.id === id)).map(id => ({ id, custom: true }))]);
  quickIDs.value = "";
}
function remove(id: string) { emit("update:modelValue", props.modelValue.filter(model => model.id !== id)); }
</script>

<style scoped>
.model-catalog-editor { display: grid; gap: 12px; min-width: 0; }
.model-catalog-tools { display: flex; gap: 10px; align-items: center; }
.model-catalog-tools > .input-group { flex: 1; min-width: 0; }
.model-catalog-tools > .btn { flex: none; }
.model-catalog-list { max-height: 264px; overflow: auto; border: 1px solid var(--border); border-radius: 12px; }
.model-catalog-row { display: flex; align-items: center; gap: 8px; padding: 6px 10px 6px 0; }
.model-catalog-row + .model-catalog-row { border-top: 1px solid var(--border); }
.model-catalog-row:hover, .model-catalog-row.selected { background: var(--surface-2); }
.model-catalog-details { display: flex; flex: 1; gap: 12px; align-items: center; min-width: 0; padding: 7px 14px; border: 0; background: transparent; color: var(--text); text-align: left; font: inherit; cursor: pointer; }
.model-catalog-details > svg { flex: none; color: var(--muted); }
.model-catalog-copy { display: grid; gap: 4px; flex: 1; min-width: 0; }
.model-catalog-id { overflow-wrap: anywhere; font-size: 13px; font-weight: 600; }
.model-catalog-details:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; border-radius: 8px; }
.model-catalog-details:disabled { cursor: default; }
.model-metadata-panel { display: grid; gap: 10px; padding: 16px; border: 1px solid var(--border-strong); border-radius: 12px; background: var(--surface-2); }
.model-metadata-heading { display: flex; align-items: start; justify-content: space-between; gap: 12px; }
.model-metadata-heading strong { font-size: 14px; }
.model-metadata-heading p { margin: 5px 0 0; }
.model-metadata-fields, .model-capability-fields { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; min-width: 0; }
.model-metadata-fields .field { min-width: 0; }
.model-metadata-fields .muted { margin-left: 4px; font-size: 11px; font-weight: 400; }
.model-metadata-panel > .hint { margin: 0; }
.model-capability-label { font-size: 12px; color: var(--muted); }
.model-capability-options { display: flex; flex-wrap: wrap; gap: 6px; }
.model-capability-option { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 34px; min-width: 52px; padding: 5px 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); color: var(--muted); font: inherit; font-size: 12px; cursor: pointer; }
.model-capability-option[aria-pressed="true"] { color: var(--accent); border-color: var(--accent); background: color-mix(in srgb, var(--accent) 9%, var(--surface)); }
.model-capability-option:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.model-metadata-error { color: var(--danger, #d2453b); margin: 0; font-size: 12px; }
.model-metadata-footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding-top: 4px; }
@media (max-width: 640px) {
  .model-catalog-tools { flex-direction: column; align-items: stretch; }
  .model-metadata-fields, .model-capability-fields { grid-template-columns: 1fr; }
  .model-metadata-panel { padding: 12px; }
  .model-capability-option { min-height: 40px; }
}
</style>
