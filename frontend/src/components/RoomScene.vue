<!-- Original generated room layers; see public/room/README.md for provenance. -->
<template>
  <div class="illustrated-room" :class="{ paused, walking }" role="img" :aria-label="description" :data-phase="frame.phase">
    <img class="room-background" :src="appearance.images.background || background" alt="" />
    <div class="character-anchor" :style="anchorStyle">
      <img v-if="customCharacter" class="custom-character character-art" :src="customCharacter" alt="" />
      <div v-else class="character-art sprite" :style="{ backgroundImage: `url(${sprites})`, backgroundPosition: `${index * 100 / 3}% 0` }"></div>
    </div>
    <div v-if="frame.pose === 'desk' && frame.phase === 'settled'" class="work-glow" :style="{ left: `${appearance.deskX}%`, top: `${appearance.deskY - 20}%` }"></div>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { RoomPose } from "../lib/room-activity";
import type { RoomAppearance } from "../lib/room-customization";
import { roomMotion, type MotionFrame } from "../lib/room-motion";
const props = defineProps<{ pose: RoomPose; paused: boolean; appearance: RoomAppearance }>();
const background = `${import.meta.env.BASE_URL}room/whale-bedroom.png`;
const sprites = `${import.meta.env.BASE_URL}room/whale-poses.png`;
const frame = ref<MotionFrame>({ pose: props.pose, location: props.pose === "desk" ? "desk" : "bed", phase: "settled" });
const motion = roomMotion(props.pose, value => { frame.value = value; });
const walking = computed(() => frame.value.phase === "walk" || frame.value.phase === "stand");
const slot = computed(() => walking.value ? "walk" : frame.value.pose);
const index = computed(() => ({ rest: 0, phone: 1, desk: 2, walk: 3 })[slot.value]);
const customCharacter = computed(() => props.appearance.images[slot.value]);
const anchorStyle = computed(() => ({
  left: `${frame.value.location === "desk" ? props.appearance.deskX : props.appearance.bedX}%`,
  top: `${frame.value.location === "desk" ? props.appearance.deskY : props.appearance.bedY}%`,
  width: `${props.appearance.size}%`
}));
const description = computed(() => `小窝角色当前${walking.value ? "移动中" : ({ rest: "休息", phone: "看手机", desk: "书桌工作" })[frame.value.pose]}。`);
watch(() => [props.pose, props.paused] as const, ([pose, paused]) => motion.target(pose, paused));
onBeforeUnmount(motion.cancel);
</script>
<style scoped>
.illustrated-room { position: relative; width: 100%; aspect-ratio: 3 / 2; overflow: hidden; background: #e8dfcf; isolation: isolate; }
.room-background { position: absolute; width: 100%; height: 100%; object-fit: fill; }
.character-anchor { position: absolute; transform: translate(-50%, -100%); transition: left 1.8s ease-in-out, top 1.8s ease-in-out; z-index: 2; }
.character-art { width: 100%; animation: whale-breathe 4s ease-in-out infinite; transform-origin: center bottom; }
.sprite { aspect-ratio: 3 / 4; background-repeat: no-repeat; background-size: 400% 100%; }
.custom-character { display: block; aspect-ratio: 3 / 4; object-fit: contain; }
.walking .character-art { animation: whale-step .32s ease-in-out infinite alternate; }
.work-glow { position: absolute; width: 9%; height: 15%; background: #fff0b240; border-radius: 50%; filter: blur(12px); pointer-events: none; mix-blend-mode: screen; }
.paused *, .paused { animation: none !important; transition: none !important; }
@keyframes whale-breathe { 50% { transform: translateY(-2px); } }
@keyframes whale-step { to { transform: translateY(-5px) rotate(-1deg); } }
@media (prefers-reduced-motion: reduce) { .illustrated-room * { animation: none !important; transition: none !important; } }
</style>
