<template>
  <div class="capacity-input">
    <input :id="id" class="input" type="number" :value="displayValue" :min="min / factor" :max="1048576 / factor" step="any" :disabled="disabled" required @input="updateValue" />
    <AppSelect :id="`${id}-unit`" :model-value="unit" :options="units" aria-label="容量单位" :disabled="disabled" @update:model-value="value => unit = value as 'MiB' | 'GiB'" />
  </div>
  <span class="hint">1 GiB = 1024 MiB；容量按 1 MiB 精度保存。</span>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue';
import AppSelect from './AppSelect.vue';
const props = withDefaults(defineProps<{ id: string; modelValue: number; min?: number; disabled?: boolean }>(), { min: 0 });
const emit = defineEmits<{ 'update:modelValue': [value: number] }>();
const units = [{ value: 'MiB', label: 'MiB' }, { value: 'GiB', label: 'GiB' }];
const unit = ref<'MiB' | 'GiB'>('GiB');
const factor = computed(() => unit.value === 'GiB' ? 1024 : 1);
const displayValue = computed(() => Number.isFinite(props.modelValue) ? props.modelValue / factor.value : '');
function updateValue(event: Event) {
  const input = event.target as HTMLInputElement;
  const raw = input.valueAsNumber * factor.value;
  const value = input.value === '' ? NaN : raw > 0 ? Math.max(1, Math.round(raw)) : raw;
  emit('update:modelValue', value);
}
</script>
<style scoped>
.capacity-input { display: flex; align-items: center; gap: 8px; }
.capacity-input > .input { min-width: 0; flex: 1; }
.capacity-input > :last-child { flex: 0 0 86px; }
</style>
