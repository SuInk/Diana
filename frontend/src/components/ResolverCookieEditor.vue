<script setup lang="ts">
import { ref, computed, onBeforeUnmount } from "vue";
import { readResolverCookieFile, saveResolverCookieFile, checkResolverCookieFile, testResolverCookieLogin } from "../api";
import { cookieSites } from "../lib/cookie-sites";
const props = defineProps<{ path: string }>();
const emit = defineEmits<{ (event: "saved", path: string): void }>();
const editing = ref(false);
const busy = ref(false);
const content = ref("");
const message = ref("");
const sites = computed(() => cookieSites(content.value));
const hasYouTube = computed(() => sites.value.some((site) => site.youtube));
let disposed = false;
onBeforeUnmount(() => { disposed = true; content.value = ""; });
async function edit() {
 busy.value = true; message.value = "";
 try {
  const result = await readResolverCookieFile();
  if (disposed) return;
  content.value = result.content; editing.value = true;
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "读取失败"; }
 finally { if (!disposed) busy.value = false; }
}
async function save() {
 busy.value = true; message.value = "";
 try {
  const result = await saveResolverCookieFile(content.value);
  if (disposed) return;
  emit("saved", result.path);
  content.value = ""; editing.value = false;
  message.value = "Cookie 已保存并启用，所有机器人共用。";
 } catch (error) { if (!disposed) message.value = error instanceof Error ? error.message : "保存失败"; }
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
function cancel() { editing.value = false; content.value = ""; message.value = ""; }
</script>

<template>
 <div class="field">
  <span>yt-dlp Cookie（多站点通用）</span>
  <span class="hint">粘贴 Netscape 格式 Cookie，由 WebUI 维护；只显示文件中已添加的网站。</span>
  <button v-if="!editing" class="btn small" type="button" :disabled="busy" @click="edit">{{ busy ? "读取中…" : "编辑 Cookie" }}</button>
  <template v-else>
   <label for="resolver-cookie-content">Cookie 文件内容</label>
   <textarea id="resolver-cookie-content" v-model="content" @input="message = ''" class="input plugin-setting-textarea" rows="10" spellcheck="false" autocomplete="off" autocapitalize="off" :disabled="busy" placeholder="# Netscape HTTP Cookie File"></textarea>
   <div v-if="sites.length" class="stack" aria-label="已添加的 Cookie 网站">
    <span>已添加 {{ sites.length }} 个域名</span>
    <div v-for="site in sites" :key="site.domain" class="hint">
     <strong>{{ site.domain }}</strong> · {{ site.count }} 条<span v-if="site.expired"> · {{ site.expired }} 条已过期</span>
     · {{ site.youtube ? "支持 YouTube 登录检测" : "支持格式检查，尚无在线登录检测" }}
    </div>
   </div>
   <span v-else class="hint">尚未识别到 Cookie 网站，粘贴后自动显示。</span>
   <span class="hint">仅检查文件格式，不代表登录有效。点击下方按钮会立即保存；取消不修改文件。</span>
   <span v-if="hasYouTube" class="hint">「检测 YouTube 登录」会将草稿中适用于 YouTube 的 Cookie 发送到 YouTube，使用已保存的解析代理，不会保存或修改 Cookie 文件。</span>
   <div class="actions">
    <button v-if="hasYouTube" class="btn" type="button" :disabled="busy || !content.trim()" @click="testLogin">检测 YouTube 登录</button>
    <button class="btn" type="button" :disabled="busy || !content.trim()" @click="check(false)">检查格式</button>
    <button class="btn" type="button" :disabled="busy || !content.trim()" @click="check(true)">格式化</button>
    <button class="btn primary" type="button" :disabled="busy || !content.trim()" @click="save">保存并启用 Cookie</button>
    <button class="btn" type="button" :disabled="busy" @click="cancel">取消</button>
   </div>
  </template>
  <span v-if="message" class="hint" role="status">{{ message }}</span>
  <details v-if="!editing">
   <summary>使用已有 Cookie 文件</summary>
   <span class="hint">自定义路径需保存插件设置后生效。编辑器只编辑 WebUI 托管文件，不读取或覆盖外部文件。</span>
   <input class="input" type="text" aria-label="已有 Cookie 文件路径" :value="props.path" @input="emit('saved', ($event.target as HTMLInputElement).value)" />
  </details>
 </div>
</template>
