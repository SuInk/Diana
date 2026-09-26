<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal v-if="confirmState.open" :title="confirmState.title" @close="settleConfirm(false)">
    <p class="confirm-message">{{ confirmState.message }}</p>
    <template #footer>
      <button class="btn" type="button" @click="settleConfirm(false)">取消</button>
      <button
        class="btn"
        :class="confirmState.danger ? 'danger' : 'primary'"
        type="button"
        @click="settleConfirm(true)"
      >
        {{ confirmState.confirmLabel }}
      </button>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import Modal from "./Modal.vue";
import { confirmState, settleConfirm } from "../confirm";
</script>

<style scoped>
.confirm-message {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13.5px;
  line-height: 1.6;
  /* 较长的警告（比如切到安全模式）按段落和条目换行，单行消息不受影响。 */
  white-space: pre-line;
}
</style>
