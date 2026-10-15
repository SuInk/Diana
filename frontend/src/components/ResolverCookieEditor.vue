<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from "vue";
import { readResolverCookieFile, saveResolverCookieFile, checkResolverCookieFile, testResolverCookieLogin } from "../api";
import { cookieSites } from "../lib/cookie-sites";
const props = defineProps<{ path: string; disabled?: boolean }>();
const emit = defineEmits<{ (event: "saved", path: string): void }>();
const editing = ref(false);
const busy = ref(false);
const content = ref("");
const message = ref("");
const original = ref("");
const loaded = ref(false);
const dirty = computed(() => content.value !== original.value);
const sites = computed(() => cookieSites(content.value));
const hasYouTube = computed(() => sites.value.some((site) => site.youtube));
let disposed = false;
onBeforeUnmount(() => { disposed = true; content.value = ""; });
onMounted(() => { void load(); });
async function load() {
 busy.value = true; message.value = "";
 try {
  const result = await readResolverCookieFile();
  if (disposed) return;
  content.value = result.content; original.value = result.content; loaded.value = true;
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "读取失败"; }
 finally { if (!disposed) busy.value = false; }
}
async function edit() {
 if (!loaded.value) await load();
 if (loaded.value) editing.value = true;
}
async function saveDraft(): Promise<boolean> {
 if (!dirty.value) return false;
 busy.value = true; message.value = "";
 try {
  const result = await saveResolverCookieFile(content.value);
  if (disposed) throw new Error("编辑器已关闭");
  emit("saved", result.path);
  original.value = content.value;
  message.value = "Cookie 已保存。";
  return true;
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "保存失败"; throw error; }
 finally { if (!disposed) busy.value = false; }
}
async function check(format: boolean) {
 busy.value = true; message.value = "";
 try {
  const result = await checkResolverCookieFile(content.value, format);
  if (disposed) return;
  if (format && result.content !== undefined) content.value = result.content;
  message.value = `${format ? "已格式化草稿，尚未保存" : "格式检查通过"}：共 ${result.count} 条，已过期 ${result.expired_count} 条。这不代表账号登录有效。`;
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "检查失败"; }
 finally { if (!disposed) busy.value = false; }
}
async function testLogin() {
 busy.value = true; message.value = "正在连接 YouTube 检测登录状态…";
 try {
  const result = await testResolverCookieLogin(content.value);
  if (disposed) return;
  const label = { valid: "登录有效", invalid: "登录无效", unknown: "无法确认" }[result.state];
  message.value = `${label}：${result.message}`;
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "检测失败，无法确认登录状态"; }
 finally { if (!disposed) busy.value = false; }
}
defineExpose({ saveDraft, hasUnsavedChanges: () => dirty.value, isBusy: () => busy.value });
</script>

<template>
 <section class="cookie-manager" aria-label="Cookie 文件">
  <div class="cookie-header">
   <div><strong>Cookie 文件</strong><p class="hint">供 yt-dlp 使用 · {{ sites.length ? `${sites.length} 个域名` : '尚未添加' }}<span v-if="dirty"> · 待保存</span></p></div>
   <button class="btn small" type="button" :disabled="busy || disabled" @click="editing ? editing = false : edit()">{{ busy ? "处理中…" : editing ? "收起编辑" : sites.length ? "编辑文件" : "粘贴 Cookie" }}</button>
  </div>
  <div v-if="sites.length" aria-label="已添加的 Cookie 网站">
   <div v-for="site in sites" :key="site.domain" class="cookie-site">
    <strong>{{ site.domain }}</strong><span class="hint">{{ site.count }} 条<span v-if="site.expired"> · {{ site.expired }} 条已过期</span></span>
    <span class="badge">{{ dirty ? '待保存' : '已配置' }}</span>
    <button v-if="site.youtube" class="btn small" type="button" :disabled="busy || disabled" @click="testLogin">检测登录</button>
   </div>
  </div>
  <template v-if="editing">
   <label class="hint" for="resolver-cookie-content">粘贴 Netscape 格式内容，随底部「保存」一起生效。</label>
   <textarea id="resolver-cookie-content" v-model="content" @input="message = ''" class="input plugin-setting-textarea" rows="6" spellcheck="false" autocomplete="off" autocapitalize="off" :disabled="busy || disabled" placeholder="# Netscape HTTP Cookie File"></textarea>
   <div class="actions">
    <button class="btn small" type="button" :disabled="busy || disabled || !content.trim()" @click="check(false)">检查格式</button>
    <button class="btn small" type="button" :disabled="busy || disabled || !content.trim()" @click="check(true)">格式化</button>
   </div>
  </template>
  <p v-if="hasYouTube" class="hint">检测登录会向 YouTube 发送相应 Cookie，使用已保存的解析代理。</p>
  <p v-if="sites.length || editing" class="hint">格式正确不代表登录有效。B 站、抖音、小红书的登录检测使用上方单独配置的凭据。</p>
  <p v-if="props.path && !props.path.endsWith('/resolver-cookies.txt')" class="hint">当前使用外部文件；保存编辑内容后将改用 WebUI 托管文件。</p>
  <p v-if="message" class="hint" role="status">{{ message }}</p>
 </section>
</template>
<style scoped>
.cookie-manager { display:grid; gap:12px; padding:14px 0; }
.cookie-header,.cookie-site { display:flex; align-items:center; gap:12px; flex-wrap:wrap; }
.cookie-header { justify-content:space-between; }
.cookie-header p { margin:4px 0 0; }
.cookie-site { padding:10px 0; border-bottom:1px solid var(--border); }
.cookie-site strong { margin-right:auto; }
</style>
