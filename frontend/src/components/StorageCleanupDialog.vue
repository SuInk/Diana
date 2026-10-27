<template>
  <Modal :title="kind === 'history' ? '清理历史媒体原件' : '清理下载缓存'" @close="!saving && emit('close')">
    <p>{{ kind === 'history' ? '删除超过保留天数或容量上限的历史原件。聊天文字和摘要保留，删除后的原件无法恢复。' : '按闲置天数或容量上限删除下载缓存，后续使用时可能需要重新下载。' }}</p>
    <p class="hint">此设置会保存为后续自动清理策略，并立即执行一次清理。清理范围仅限此类文件。</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="loading" role="status">正在读取当前策略…</p>
    <form v-else class="form-grid" @submit.prevent="submit">
      <div class="field"><label for="storage-clean-days">保留天数</label><input id="storage-clean-days" v-model.number="days" class="input" type="number" min="-1" max="36500" :disabled="saving" /><span class="hint">-1 表示不按时间清理；按天清理时至少保留 1 天。</span></div>
      <div class="field"><label for="storage-clean-limit">容量上限</label><CapacityInput id="storage-clean-limit" v-model="maxMB" :disabled="saving" /><span class="hint">0 表示不限制容量。</span></div>
      <p v-if="days === -1 && maxMB === 0" class="hint field wide">当前策略不删除任何文件，请设置保留天数或容量上限。</p>
      <div class="field wide"><button class="btn primary" type="submit" :disabled="saving || !loaded || !valid">{{ saving ? '清理中…' : '保存策略并立即清理' }}</button></div>
    </form>
  </Modal>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import CapacityInput from "./CapacityInput.vue";
import Modal from "./Modal.vue";
import { getHistoryMediaPolicy, getMediaCachePolicy, saveHistoryMediaPolicy, saveMediaCachePolicy } from "../api";
import { askConfirm } from "../lib/confirm";
import { toastSuccess } from "../lib/toast";
const props = defineProps<{ kind: 'history' | 'cache' }>();
const emit = defineEmits<{ close: []; cleaned: [] }>();
const loading = ref(true), loaded = ref(false), saving = ref(false), error = ref('');
const days = ref(-1), maxMB = ref(0);
const valid = computed(() => Number.isInteger(days.value) && days.value >= -1 && days.value <= 36500 && days.value !== 0 && Number.isInteger(maxMB.value) && maxMB.value >= 0 && maxMB.value <= 1048576 && (days.value !== -1 || maxMB.value > 0));
onMounted(async () => {
  try {
    const policy = await (props.kind === 'history' ? getHistoryMediaPolicy() : getMediaCachePolicy());
    days.value = policy.retention_days === 0 ? -1 : policy.retention_days; maxMB.value = policy.max_mb; loaded.value = true;
  } catch (e) { error.value = e instanceof Error ? e.message : '读取策略失败'; }
  finally { loading.value = false; }
});
async function submit() {
  if (!loaded.value || !valid.value || saving.value) return;
  if (!await askConfirm({ title: '确认清理', message: `${props.kind === 'history' ? '历史媒体原件' : '下载缓存'}：保留天数 ${days.value === -1 ? '不限' : days.value}，容量上限 ${maxMB.value === 0 ? '不限' : maxMB.value + ' MiB'}。超过限制的文件将被删除，此策略也会用于后续自动清理。`, confirmLabel: '保存并清理', danger: true })) return;
  saving.value = true; error.value = '';
  try {
    await (props.kind === 'history' ? saveHistoryMediaPolicy : saveMediaCachePolicy)({ retention_days: days.value, max_mb: maxMB.value });
    toastSuccess(props.kind === 'history' ? '历史媒体清理完成' : '下载缓存清理策略已应用');
    emit('cleaned'); emit('close');
  } catch (e) { error.value = e instanceof Error ? e.message : '清理失败'; }
  finally { saving.value = false; }
}
</script>
