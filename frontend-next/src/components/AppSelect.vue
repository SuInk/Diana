<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div ref="rootRef" class="app-select" :class="{ open }">
    <button :id="id" type="button" class="app-select-trigger" :disabled="disabled" @click="toggle" @keydown="onKeydown">
      <span class="app-select-value">{{ currentLabel }}</span>
      <ChevronDown :size="14" class="app-select-chevron" aria-hidden="true" />
    </button>
    <div v-if="open" class="app-select-menu" role="listbox">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="app-select-item"
        :class="{ active: option.value === modelValue }"
        role="option"
        :aria-selected="option.value === modelValue"
        @click="pick(option.value)"
      >
        <Check v-if="option.value === modelValue" :size="14" aria-hidden="true" />
        <span v-else class="app-select-item-pad" aria-hidden="true"></span>
        <span class="app-select-item-label">
          {{ option.label }}
          <small v-if="option.hint" class="muted">{{ option.hint }}</small>
        </span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { Check, ChevronDown } from "@lucide/vue";

export interface AppSelectOption {
  value: string;
  label: string;
  hint?: string;
}

const props = defineProps<{
  modelValue: string;
  options: AppSelectOption[];
  id?: string;
  disabled?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [string] }>();

const open = ref(false);
const rootRef = ref<HTMLElement | null>(null);

const currentLabel = computed(() => props.options.find((option) => option.value === props.modelValue)?.label ?? props.modelValue);

function toggle(): void {
  open.value = !open.value;
}

function pick(value: string): void {
  emit("update:modelValue", value);
  open.value = false;
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    open.value = false;
    return;
  }
  // 上下键在选项间移动，无需展开也能快速切换。
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const index = props.options.findIndex((option) => option.value === props.modelValue);
    const next = event.key === "ArrowDown" ? index + 1 : index - 1;
    if (next >= 0 && next < props.options.length) {
      emit("update:modelValue", props.options[next].value);
    }
  }
}

function onDocumentClick(event: MouseEvent): void {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) {
    open.value = false;
  }
}

onMounted(() => document.addEventListener("mousedown", onDocumentClick));
onBeforeUnmount(() => document.removeEventListener("mousedown", onDocumentClick));
</script>
