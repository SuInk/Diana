<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from "vue";
import { listYTDLPSupportedSites } from "../api";
const entries = ref<string[]>([]);
const query = ref("");
const page = ref(0);
const busy = ref(false);
const error = ref("");
let disposed = false;
onBeforeUnmount(() => { disposed = true; });
const matches = computed(() => entries.value.filter((entry) => entry.toLowerCase().includes(query.value.trim().toLowerCase())));
const pages = computed(() => Math.max(1, Math.ceil(matches.value.length / 100)));
const visible = computed(() => matches.value.slice(page.value * 100, (page.value + 1) * 100));
watch(query, () => { page.value = 0; });
async function load() {
 busy.value = true; error.value = "";
 try {
  const result = await listYTDLPSupportedSites();
  if (!disposed) { entries.value = result.extractors; page.value = 0; }
 } catch (err) { if (!disposed) error.value = err instanceof Error ? err.message : "读取失败"; }
 finally { if (!disposed) busy.value = false; }
}
</script>

<template>
 <details>
  <summary>yt-dlp 支持的网站（完整列表）</summary>
  <div class="stack">
   <span class="hint">读取本机版本的全部内置解析器，可搜索和翻页；同一网站可能有视频、列表等多个条目。列出不代表网站当前可用，也不代表 Diana 已启用该网站解析。</span>
   <a href="https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md" target="_blank" rel="noopener noreferrer">查看 yt-dlp 官方完整列表</a>
   <button class="btn small" type="button" :disabled="busy" @click="load">{{ busy ? "读取中…" : entries.length ? "刷新本机列表" : "读取本机完整列表" }}</button>
   <span v-if="error" class="hint" role="status">{{ error }}</span>
   <template v-if="entries.length">
    <input v-model="query" class="input" type="search" aria-label="搜索 yt-dlp 网站" placeholder="搜索网站名称，如 YouTube、Twitter、BiliBili" />
    <span class="hint" role="status">共 {{ entries.length }} 个解析器，匹配 {{ matches.length }} 个</span>
    <ul class="extractor-list"><li v-for="entry in visible" :key="entry">{{ entry }}</li></ul>
    <div class="actions">
     <button class="btn small" type="button" :disabled="page === 0" @click="page--">上一页</button>
     <span>{{ page + 1 }} / {{ pages }}</span>
     <button class="btn small" type="button" :disabled="page + 1 >= pages" @click="page++">下一页</button>
    </div>
   </template>
  </div>
 </details>
</template>

<style scoped>
.extractor-list { max-height: 18rem; overflow: auto; padding-left: 1.5rem; overflow-wrap: anywhere; }
</style>
