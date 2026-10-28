<template>
  <div class="md-diagram">
    <div class="md-code-bar">
      <span>{{ showSource ? "mermaid" : "图表" }}</span>
      <button v-if="svg" type="button" class="md-code-copy md-diagram-toggle" :aria-pressed="showSource" @click="showSource = !showSource">{{ showSource ? "看图" : "源码" }}</button>
    </div>
    <!-- mermaid 用 strict 安全级别渲染，输出的 SVG 已经过它自带的清洗，标签里的脚本和事件不会保留。 -->
    <div v-if="svg && !showSource" class="md-diagram-svg" role="img" :aria-label="label" v-html="svg" />
    <pre v-else><code>{{ source }}</code></pre>
    <p v-if="failed" class="md-diagram-error">图表语法有误，先显示源码。</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { theme } from "../lib/theme";

const props = defineProps<{ source: string }>();
const svg = ref("");
const failed = ref(false);
const showSource = ref(false);
const label = computed(() => `图表：${props.source.split("\n")[0].trim()}`);

let seq = 0;
// mermaid 有一两 MB，回复里真有图才加载；动态导入有模块缓存，多张图只下载一次。
let loader: Promise<typeof import("mermaid").default> | null = null;
function loadMermaid() {
  loader ??= import("mermaid").then((module) => module.default);
  return loader;
}

async function render(): Promise<void> {
  const run = ++seq;
  let id = "";
  try {
    const mermaid = await loadMermaid();
    const dark = document.documentElement.dataset.theme === "dark";
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: dark ? "dark" : "default", fontFamily: "inherit" });
    // 不用 crypto.randomUUID：局域网 http 打开的 WebUI 不是安全上下文，没有这个接口。
    id = `md-diagram-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    const { svg: output } = await mermaid.render(id, props.source);
    if (run !== seq) return;
    svg.value = output;
    failed.value = false;
  } catch {
    // 语法错误时 mermaid 会把报错图留在 body 里，要自己清掉。
    document.getElementById(`d${id}`)?.remove();
    if (run !== seq) return;
    svg.value = "";
    failed.value = true;
  }
}

onMounted(render);
watch(() => [props.source, theme.mode], render);
</script>

<style scoped>
.md-diagram { margin: 0 0 .75em; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-2); overflow: hidden; }
.md-code-bar { display: flex; align-items: center; justify-content: space-between; padding: 4px 6px 4px 12px; border-bottom: 1px solid var(--border); color: var(--muted); font-size: 11px; }
.md-diagram-toggle { padding: 2px 8px; border: 0; border-radius: 6px; background: transparent; color: inherit; font: inherit; cursor: pointer; }
.md-diagram-toggle:hover { background: var(--surface); color: var(--text); }
.md-diagram-svg { display: flex; justify-content: center; padding: 12px; overflow-x: auto; background: var(--surface); }
.md-diagram-svg :deep(svg) { max-width: 100%; height: auto; }
pre { margin: 0; padding: 10px 12px; overflow-x: auto; line-height: 1.6; }
pre code { font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace); font-size: .88em; white-space: pre; }
.md-diagram-error { margin: 0; padding: 0 12px 10px; color: var(--muted); font-size: 11px; }
</style>
