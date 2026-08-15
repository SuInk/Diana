<template>
  <div class="toast-host" aria-live="polite">
    <div v-for="toast in toastState.toasts" :key="toast.id" class="toast" :class="toast.kind">
      <CheckCircle2 v-if="toast.kind === 'success'" :size="16" class="text-ok" aria-hidden="true" />
      <TriangleAlert v-else-if="toast.kind === 'error'" :size="16" class="text-err" aria-hidden="true" />
      <Sparkles v-else :size="16" aria-hidden="true" />
      <span>{{ toast.message }}</span>
      <button type="button" aria-label="关闭提示" @click="dismiss(toast.id)">
        <X :size="14" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CheckCircle2, Sparkles, TriangleAlert, X } from "@lucide/vue";
import { dismiss, toastState } from "../toast";
</script>
