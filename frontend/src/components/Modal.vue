<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Teleport to="body">
    <!-- 遮罩不再关闭弹窗：这些弹窗里几乎都是表单，误点一下外面就丢掉一屏输入。
         关闭仍然有右上角按钮和 Esc 两条明确的路径。 -->
    <!-- side 是右侧抽屉：不挡住页面，后面的列表照样能点，用来边看全局边改一项。 -->
    <div class="modal-backdrop" :class="{ side }">
      <div ref="modalRef" class="modal" :class="{ wide, side, 'fixed-height': fixedHeight }" role="dialog" :aria-modal="side ? 'false' : 'true'" :aria-label="title" @pointerdown.capture="emit('pointerdown', $event)">
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
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { X } from "@lucide/vue";
import { lockBodyScroll, releaseBodyScroll } from "../lib/scrollLock";

const props = defineProps<{ title: string; wide?: boolean; side?: boolean; fixedHeight?: boolean; initialFocus?: string }>();
const emit = defineEmits<{ close: []; pointerdown: [event: PointerEvent] }>();
const modalRef = ref<HTMLElement | null>(null);
let previousFocus: HTMLElement | null = null;

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    const dialogs = document.querySelectorAll('.modal[aria-modal="true"]');
    // 抽屉上面叠着弹窗时，Esc 先关弹窗；没有弹窗了才轮到抽屉。
    if (props.side ? dialogs.length > 0 : dialogs[dialogs.length - 1] !== modalRef.value) return;
    emit("close");
  }
}

onMounted(() => {
  document.addEventListener("keydown", onKeydown);
  if (!props.side) lockBodyScroll();
  if (props.initialFocus) {
    previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    modalRef.value?.querySelector<HTMLElement>(props.initialFocus)?.focus({ preventScroll: true });
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKeydown);
  if (!props.side) releaseBodyScroll();
  if (previousFocus) void nextTick(() => { if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true }); });
});
</script>
