<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount } from "vue";
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
const loginState = ref<"valid" | "invalid" | "unknown" | "checking" | "">("");
const youtubeStatus = computed(() => {
 if (loginState.value === "checking") return { label: "检测中…", color: "warn" };
 if (loginState.value === "valid") return { label: "登录有效", color: "ok" };
 if (loginState.value === "invalid") return { label: "登录失效", color: "err" };
 if (loginState.value === "unknown") return { label: "无法确认", color: "warn" };
 if (dirty.value) return { label: "待保存 / 待检测", color: "warn" };
 if (!hasYouTube.value) return { label: props.path ? "文件待检查" : "未配置", color: props.path ? "warn" : "" };
 return { label: "待检测", color: "warn" };
});
watch(content, () => { loginState.value = ""; message.value = ""; }, { flush: "sync" });
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
 busy.value = true; loginState.value = "checking"; message.value = "正在连接 YouTube 检测登录状态…";
 try {
  const result = await testResolverCookieLogin(content.value);
  if (disposed) return;
  loginState.value = result.state;
  const label = { valid: "登录有效", invalid: "登录无效", unknown: "无法确认" }[result.state];
  message.value = `${label}：${result.message}`;
 } catch (error) { if (!disposed) { loginState.value = "unknown"; message.value = error instanceof Error ? error.message : "检测失败，无法确认登录状态"; } }
 finally { if (!disposed) busy.value = false; }
}
defineExpose({ saveDraft, hasUnsavedChanges: () => dirty.value, isBusy: () => busy.value });
</script>

<template>
 <section class="cookie-manager" aria-label="YouTube Cookie">
  <div class="cookie-header">
   <slot name="platform"><strong>YouTube</strong></slot>
   <span class="badge" :class="youtubeStatus.color" role="status">{{ youtubeStatus.label }}</span>
   <div class="cookie-actions">
    <button class="btn small" type="button" :disabled="busy || disabled || !hasYouTube" @click="testLogin">{{ loginState === 'checking' ? '检测中…' : '检测' }}</button>
    <button class="btn small" type="button" :disabled="busy || disabled" :aria-expanded="editing" @click="editing ? editing = false : edit()">{{ editing ? "收起" : "编辑" }}</button>
   </div>
  </div>
  <div v-if="editing" class="cookie-editor">
   <label for="resolver-cookie-content">Cookie 内容</label>
   <p class="hint">粘贴 Netscape 格式 Cookie，随底部「保存」生效。此文件由 yt-dlp 共用，可包含多个网站。</p>
   <textarea id="resolver-cookie-content" v-model="content" class="input plugin-setting-textarea" rows="6" spellcheck="false" autocomplete="off" autocapitalize="off" :disabled="busy || disabled" placeholder="# Netscape HTTP Cookie File"></textarea>
   <div class="actions">
    <button class="btn small" type="button" :disabled="busy || disabled || !content.trim()" @click="check(false)">检查格式</button>
    <button class="btn small" type="button" :disabled="busy || disabled || !content.trim()" @click="check(true)">格式化</button>
   </div>
   <div v-if="sites.length" aria-label="文件中的网站">
    <p v-for="site in sites" :key="site.domain" class="hint"><strong>{{ site.domain }}</strong> · {{ site.count }} 条<span v-if="site.expired"> · {{ site.expired }} 条已过期</span></p>
   </div>
   <p class="hint">检测会向 YouTube 发送相应 Cookie，使用已保存的代理。格式正确不代表登录有效。</p>
   <p v-if="props.path && !props.path.endsWith('/resolver-cookies.txt')" class="hint">当前使用外部文件；保存编辑内容后将改用托管文件。此编辑器不读取外部文件。</p>
  </div>
  <p v-if="message" class="hint" role="status">{{ message }}</p>
 </section>
</template>
<style scoped>
.cookie-manager { padding:10px 0; border-bottom:1px solid var(--border); }
.cookie-header { display:grid; grid-template-columns:minmax(0,1fr) auto auto; align-items:center; gap:10px; }
.cookie-actions { display:flex; justify-content:flex-end; gap:6px; }
.cookie-actions .btn { padding:4px 7px; font-size:11px; }
.cookie-editor { display:grid; gap:12px; padding:16px; margin-top:12px; border-radius:10px; background:var(--bg); }
.cookie-editor p { margin:0; }
@media(max-width:560px) {
 .cookie-header { grid-template-columns:1fr auto; gap:10px; }
 .cookie-actions { grid-column:2; }
}
</style>
