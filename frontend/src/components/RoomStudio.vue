<template>
  <div class="room-studio">
    <RoomScene :pose="pose" :paused="paused || editing" :appearance="appearance" />
    <button v-if="canEdit" class="customize" type="button" :aria-expanded="editing" @click="editing = !editing">{{ editing ? '关闭自定义' : '自定义小窝' }}</button>
    <section v-if="editing" class="room-editor" aria-label="自定义小窝">
      <h3>布置你的小窝</h3>
      <p>仅保存在当前浏览器，按机器人隔离。图片在本地处理，不上传。预览动作不会改变 Agent 状态。交还或离开页面会放弃未保存的布置。</p>
      <label>编辑位置
        <AppSelect :model-value="preview" :options="previewOptions" aria-label="编辑位置" @update:model-value="(value) => { preview = value as RoomPose; }" />
      </label>
      <div class="uploads">
        <label v-for="slot in slots" :key="slot.key">{{ slot.label }}
          <input type="file" accept="image/png,image/jpeg,image/webp" :disabled="busy" @change="upload($event, slot.key)" />
          <span>{{ appearance.images[slot.key] ? '已使用自定义图片' : '使用默认素材' }}</span>
          <button v-if="appearance.images[slot.key]" type="button" class="btn ghost small" @click="removeImage(slot.key)">恢复这张</button>
        </label>
      </div>
      <p>每张不超过 700 KB、最长边 4096px。角色推荐透明 PNG / WebP；各动作可分别上传，没上传的动作沿用默认鲸鱼娘。背景固定完整显示为 3:2，建议同等比例。</p>
      <label v-for="field in fields" :key="field.key">{{ field.label }} <output>{{ appearance[field.key] }}%</output>
        <input v-model.number="appearance[field.key]" type="range" :min="field.key === 'size' ? 15 : 10" :max="field.key === 'size' ? 45 : 90" step="1" />
      </label>
      <p role="status" aria-live="polite">{{ feedback }}</p>
      <div class="editor-actions"><button class="btn primary" type="button" :disabled="busy" @click="save">保存到此浏览器</button><button class="btn" type="button" :disabled="busy" @click="reset">恢复默认</button></div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import RoomScene from "./RoomScene.vue";
import AppSelect from "./AppSelect.vue";
import type { RoomPose } from "../lib/room-activity";
import { defaultRoomAppearance, normalizeRoomAppearance, readRoomImage, roomAppearanceKey, type RoomImageSlot } from "../lib/room-customization";
const props = defineProps<{ profile: string; pose: RoomPose; paused: boolean; canEdit?: boolean }>();
const appearance = ref(defaultRoomAppearance());
const editing = ref(false);
const preview = ref<RoomPose>("phone");
const previewOptions = [{ value: "rest", label: "床边 · 休息" }, { value: "phone", label: "床边 · 手机" }, { value: "desk", label: "书桌" }];
const pose = computed(() => editing.value ? preview.value : props.pose);
const feedback = ref("");
const busy = ref(false);
let generation = 0;
const slots: { key: RoomImageSlot; label: string }[] = [
  { key: "background", label: "房间背景" }, { key: "rest", label: "休息角色" },
  { key: "phone", label: "手机角色" }, { key: "desk", label: "书桌角色" }, { key: "walk", label: "移动角色" }
];
const fields = [
  { key: "size", label: "角色大小" }, { key: "bedX", label: "床边左右" }, { key: "bedY", label: "床边上下" },
  { key: "deskX", label: "书桌左右" }, { key: "deskY", label: "书桌上下" }
] as const;
watch(() => props.profile, () => {
  generation++; busy.value = false; editing.value = false; feedback.value = "";
  appearance.value = defaultRoomAppearance();
  try { appearance.value = normalizeRoomAppearance(JSON.parse(localStorage.getItem(roomAppearanceKey(props.profile)) || "null")); }
  catch { feedback.value = "未能读取本地布置，暂用默认房间。"; }
}, { immediate: true });
onBeforeUnmount(() => { generation++; });
async function upload(event: Event, slot: RoomImageSlot) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  const token = ++generation;
  busy.value = true;
  try {
    const data = await readRoomImage(file);
    if (token !== generation) return;
    appearance.value.images[slot] = data;
    feedback.value = "已预览，点击保存后会在下次打开时恢复。";
  } catch (error) { if (token === generation) feedback.value = error instanceof Error ? error.message : "图片读取失败"; }
  finally { if (token === generation) busy.value = false; }
}
function removeImage(slot: RoomImageSlot) { delete appearance.value.images[slot]; feedback.value = "已恢复这张默认素材，请保存。"; }
function save() {
  try {
    localStorage.setItem(roomAppearanceKey(props.profile), JSON.stringify(appearance.value));
    feedback.value = "已保存到当前浏览器。";
  } catch { feedback.value = "保存失败：浏览器存储空间不足或禁止存储。现有已保存的布置未覆盖，请减少图片大小。"; }
}
function reset() { appearance.value = defaultRoomAppearance(); feedback.value = "已预览默认房间；点击保存确认恢复。"; }
</script>

<style scoped>
.room-studio { position: relative; }
.customize { position: absolute; top: 12px; right: 12px; padding: 8px 12px; border: 1px solid #ffffff80; border-radius: 20px; color: #203d31; background: #fffdf2ed; cursor: pointer; font: inherit; font-size: 12px; }
.room-editor { position: absolute; z-index: 5; right: 12px; top: 54px; width: min(390px, calc(100% - 24px)); max-height: calc(100% - 70px); overflow: auto; padding: 16px; border-radius: 12px; background: var(--surface); color: var(--text); box-shadow: var(--shadow-lg); box-sizing: border-box; }
.room-editor h3 { margin: 0; }
.room-editor p, .uploads span { font-size: 11px; line-height: 1.6; color: var(--muted); }
.room-editor label { display: block; margin: 12px 0; font-size: 12px; }
.room-editor input, .room-editor select { display: block; width: 100%; box-sizing: border-box; margin-top: 5px; }
.room-editor input[type=range] { accent-color: var(--accent); }
.room-editor output { float: right; }
.editor-actions { display: flex; flex-wrap: wrap; gap: 8px; }
@media (max-width: 600px) { .room-editor { position: relative; top: auto; right: auto; margin: 0; width: 100%; max-height: 440px; border-radius: 0; } }
</style>
