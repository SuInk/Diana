<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Teleport to="body">
    <!-- 遮罩不再关闭弹窗：这些弹窗里几乎都是表单，误点一下外面就丢掉一屏输入。
         关闭仍然有右上角按钮和 Esc 两条明确的路径。 -->
    <div class="modal-backdrop">
      <div class="modal" :class="{ wide, 'fixed-height': fixedHeight }" role="dialog" aria-modal="true" :aria-label="title">
        <header class="modal-header">
          <h2>{{ title }}</h2>
          <button class="btn ghost icon-only small" type="button" aria-label="关闭" @click="emit('close')">
            <X :size="16" aria-hidden="true" />
          </button>
        </header>
        <div class="modal-body">
          <slot />
        </div>
        <footer v-if="$slots.footer" class="modal-footer">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted } from "vue";
import { X } from "@lucide/vue";
import { lockBodyScroll, releaseBodyScroll } from "../scrollLock";

// fixedHeight：内容会随标签页、筛选结果伸缩的弹窗，高度钉在上限，免得切一下跳一下。
defineProps<{ title: string; wide?: boolean; fixedHeight?: boolean }>();
const emit = defineEmits<{ close: [] }>();

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    emit("close");
  }
}

onMounted(() => {
  document.addEventListener("keydown", onKeydown);
  lockBodyScroll();
});

onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKeydown);
  releaseBodyScroll();
});
</script>
