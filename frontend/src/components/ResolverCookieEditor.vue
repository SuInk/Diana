<script setup lang="ts">
import { ref, onBeforeUnmount } from "vue";
import { readResolverCookieFile, saveResolverCookieFile } from "../api";
const props = defineProps<{ path: string }>();
const emit = defineEmits<{ (event: "saved", path: string): void }>();
const editing = ref(false);
const busy = ref(false);
const content = ref("");
const message = ref("");
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
function cancel() { editing.value = false; content.value = ""; message.value = ""; }
</script>

<template>
 <div class="field">
  <span>yt-dlp Cookie</span>
  <span class="hint">粘贴 Netscape 格式的 Cookie，文件由 WebUI 维护，无需填写路径。</span>
  <button v-if="!editing" class="btn small" type="button" :disabled="busy" @click="edit">{{ busy ? "读取中…" : "编辑 Cookie" }}</button>
  <template v-else>
   <label for="resolver-cookie-content">Cookie 文件内容</label>
   <textarea id="resolver-cookie-content" v-model="content" class="input plugin-setting-textarea" rows="10" spellcheck="false" autocomplete="off" autocapitalize="off" :disabled="busy" placeholder="# Netscape HTTP Cookie File"></textarea>
   <span class="hint">仅检查文件格式，不代表登录有效。点击下方按钮会立即保存；取消不修改文件。</span>
   <div class="actions">
    <button class="btn primary" type="button" :disabled="busy || !content.trim()" @click="save">{{ busy ? "保存中…" : "保存并启用 Cookie" }}</button>
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
