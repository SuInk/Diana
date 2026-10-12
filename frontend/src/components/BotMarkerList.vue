<script setup lang="ts">
import { ref } from "vue";
import { Plus, X } from "@lucide/vue";

const props = defineProps<{ modelValue?: string[]; inheritedIds?: string[] }>();
const emit = defineEmits<{ "update:modelValue": [ids:string[]] }>();
const draft = ref("");
function add() {
  const ids = draft.value.split(/[\s,，]+/).filter(Boolean);
  if (!ids.length) return;
  emit("update:modelValue",[...new Set([...(props.modelValue ?? []),...ids])]);
  draft.value="";
}
function remove(id:string) { emit("update:modelValue",(props.modelValue ?? []).filter(value=>value!==id)); }
</script>

<template>
  <div class="bot-markers">
    <div v-if="inheritedIds?.length" class="inherited-marks">机器人范围：{{ inheritedIds.join('、') }}</div>
    <div class="marked-accounts">
      <span v-for="id in modelValue" :key="id" class="marked-account">
        <span>{{ id }}</span>
        <button type="button" class="btn icon small" :aria-label="`取消标记 ${id}`" :title="`取消标记 ${id}`" @click="remove(id)"><X :size="14" /></button>
      </span>
    </div>
    <div class="marker-entry">
      <input v-model="draft" class="input" aria-label="待标记的账号 ID" placeholder="账号 ID，逗号分隔" @keydown.enter.prevent="add" />
      <button class="btn icon" type="button" aria-label="添加机器人标记" title="添加机器人标记" :disabled="!draft.trim()" @click="add"><Plus :size="16" /></button>
    </div>
  </div>
</template>

<style scoped>
.bot-markers { display:grid; gap:8px; min-width:0; }
.marked-accounts { display:flex; flex-wrap:wrap; gap:6px; }
.marked-account { display:inline-flex; align-items:center; gap:6px; max-width:100%; border:1px solid var(--border); border-radius:4px; padding-left:8px; }
.marked-account span,.inherited-marks { overflow-wrap:anywhere; min-width:0; }
.inherited-marks { font-size:12px; color:var(--muted); }
.marker-entry { display:flex; gap:8px; }
.marker-entry input { min-width:0; flex:1; }
</style>
