<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->
<template>
  <section class="card" role="alert">
    <EmptyState title="页面加载未完成" hint="页面资源可能已更新，或网络连接中断。重新载入后再试。">
      <button class="btn" type="button" @click="reload">重新载入</button>
    </EmptyState>
  </section>
</template>

<script setup lang="ts">
import EmptyState from "./EmptyState.vue";

function reload(): void {
  window.location.reload();
}
</script>
