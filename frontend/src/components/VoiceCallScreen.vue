<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Teleport to="body">
    <div class="vc-screen" role="dialog" aria-modal="true" :aria-label="`和 ${displayName} 语音通话`" @keydown.esc="hangup">
      <div class="vc-backdrop" :style="avatar ? { backgroundImage: `url(${avatar})` } : undefined" aria-hidden="true" />
      <header class="vc-top">
        <span class="vc-top-label"><Lock :size="12" aria-hidden="true" />语音通话</span>
      </header>

      <div class="vc-peer" :class="[stageState, { 'user-speaking': userSpeaking || holding }]">
        <div class="vc-avatar-wrap">
          <span class="vc-ring" aria-hidden="true" />
          <span class="vc-ring delay" aria-hidden="true" />
          <img v-if="avatar" class="vc-avatar" :src="avatar" alt="" />
          <span v-else class="vc-avatar vc-avatar-fallback" aria-hidden="true">{{ displayName.slice(0, 1) }}</span>
        </div>
        <h2 class="vc-name">{{ displayName }}</h2>
        <p class="vc-status" role="status" aria-live="polite">{{ statusLabel }}</p>
      </div>

      <section class="vc-captions" aria-label="通话字幕" aria-live="polite" @click="canInterrupt && interrupt()">
        <p v-if="problem" class="vc-problem">{{ problem }}</p>
        <template v-else>
          <p v-for="line in captions" :key="line.key" class="vc-caption" :class="line.who">{{ line.text }}</p>
          <button v-if="canInterrupt" class="vc-interrupt" type="button" @click.stop="interrupt"><Hand :size="14" aria-hidden="true" />轻点打断</button>
        </template>
      </section>

      <form v-if="typing && inCall" class="vc-typing" @submit.prevent="sendText">
        <input ref="typingInput" v-model="typed" type="text" placeholder="打字说一句" aria-label="打字说一句" enterkeyhint="send" />
        <button type="submit" :disabled="!typed.trim()" aria-label="发送"><ArrowUp :size="18" aria-hidden="true" /></button>
      </form>

      <div class="vc-controls">
        <div class="vc-row">
          <button class="vc-btn" :class="{ on: muted }" type="button" :disabled="!inCall" :aria-pressed="muted" @click="muted = !muted">
            <span class="vc-circle"><MicOff v-if="muted" :size="24" aria-hidden="true" /><Mic v-else :size="24" aria-hidden="true" /></span>
            <span class="vc-label">{{ muted ? "已静音" : "麦克风已开" }}</span>
          </button>
          <button class="vc-btn" :class="{ on: pushToTalk }" type="button" :disabled="!inCall" :aria-pressed="pushToTalk" @click="togglePushToTalk">
            <span class="vc-circle"><Hand :size="24" aria-hidden="true" /></span>
            <span class="vc-label">对讲</span>
          </button>
          <button class="vc-btn" :class="{ on: typing }" type="button" :disabled="!inCall" :aria-pressed="typing" @click="toggleTyping">
            <span class="vc-circle"><Keyboard :size="24" aria-hidden="true" /></span>
            <span class="vc-label">打字</span>
          </button>
        </div>
        <div class="vc-row vc-row-main">
          <button
            v-if="pushToTalk && inCall"
            class="vc-btn vc-ptt"
            :class="{ on: holding }"
            type="button"
            @pointerdown.prevent="pressTalk"
            @pointerup.prevent="releaseTalk"
            @pointercancel.prevent="releaseTalk"
            @pointerleave="holding && releaseTalk()"
            @contextmenu.prevent
          >
            <span class="vc-circle"><Mic :size="26" aria-hidden="true" /></span>
            <span class="vc-label">{{ holding ? "松开发送" : "按住说话" }}</span>
          </button>
          <button class="vc-btn vc-hangup" type="button" @click="hangup">
            <span class="vc-circle"><PhoneOff :size="28" aria-hidden="true" /></span>
            <span class="vc-label">{{ inCall || connecting ? "挂断" : "关闭" }}</span>
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { ArrowUp, Hand, Keyboard, Lock, Mic, MicOff, PhoneOff } from "@lucide/vue";
import { getVoiceCallInfo } from "../api";
import {
  applyCallMessage,
  CALL_SAMPLE_RATE,
  callSocketURL,
  downsample,
  floatToPCM16,
  SentenceAudioCollector,
  type CallAudio,
  type CallServerMessage,
  type CallState,
  type CallTurnView
} from "../lib/voice-call";

const props = defineProps<{
  profile: string;
  name?: string;
  avatar?: string;
  /** 点击电话按钮时就建好的音频上下文和麦克风请求：手机浏览器只认手势里发起的。 */
  audio: CallAudio;
}>();
const emit = defineEmits<{ close: [] }>();

const botName = ref("");
const displayName = computed(() => botName.value || props.name || "机器人");
const problem = ref("");
const connecting = ref(true);
const inCall = ref(false);
const ended = ref(false);
const callState = ref<CallState>("listening");
const userSpeaking = ref(false);
const holding = ref(false);
const muted = ref(false);
const pushToTalk = ref(false);
const typing = ref(false);
const typed = ref("");
const typingInput = ref<HTMLInputElement | null>(null);
const turns = ref<CallTurnView[]>([]);
const elapsed = ref(0);
let clock: ReturnType<typeof setInterval> | undefined;

const stageState = computed(() => (inCall.value ? callState.value : "idle"));
const canInterrupt = computed(() => inCall.value && (callState.value === "speaking" || callState.value === "thinking"));
const duration = computed(() => {
  const minutes = Math.floor(elapsed.value / 60);
  const seconds = elapsed.value % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
});
const statusLabel = computed(() => {
  if (problem.value) return ended.value && elapsed.value ? `通话结束 ${duration.value}` : "未接通";
  if (ended.value) return `通话结束 ${duration.value}`;
  if (connecting.value) return "正在呼叫…";
  if (muted.value) return `${duration.value} · 已静音`;
  if (userSpeaking.value || holding.value) return `${duration.value} · 在听`;
  if (callState.value === "thinking") return `${duration.value} · 在想…`;
  if (callState.value === "speaking") return `${duration.value} · 在说话`;
  return duration.value;
});
// 字幕只留最近两轮，像通话里的实时字幕，完整记录在机器人的私聊记录里。
const captions = computed(() =>
  turns.value.slice(-2).flatMap((item) => [
    ...(item.user ? [{ key: `${item.turn}u`, who: "user", text: item.user }] : []),
    ...(item.bot.length ? [{ key: `${item.turn}b`, who: item.status === "interrupted" ? "bot cut" : "bot", text: item.bot.join("") }] : [])
  ])
);

let socket: WebSocket | null = null;
let micStream: MediaStream | null = null;
let micSource: MediaStreamAudioSourceNode | null = null;
let processor: ScriptProcessorNode | null = null;
let playbackSources: AudioBufferSourceNode[] = [];
let playbackCursor = 0;
let playbackChain: Promise<void> = Promise.resolve();
let playbackEpoch = 0;
const collector = new SentenceAudioCollector();
const audioContext = props.audio.context;

onMounted(() => void dial());

async function dial(): Promise<void> {
  try {
    const info = await getVoiceCallInfo(props.profile);
    botName.value = info.bot_name;
    if (info.problem) return fail(`${info.problem}。到「提供商」页的模型分配里设置后再打。`);
  } catch (err) {
    return fail(err instanceof Error ? err.message : String(err));
  }
  if (!window.isSecureContext) return fail("浏览器只在 HTTPS 或本机地址下允许使用麦克风。");
  try {
    micStream = await props.audio.mic;
  } catch (err) {
    return fail(`拿不到麦克风：${err instanceof Error ? err.message : String(err)}`);
  }
  if (ended.value) return cleanup();
  const ws = new WebSocket(callSocketURL(props.profile));
  ws.binaryType = "arraybuffer";
  socket = ws;
  // 一直按服务端分句、允许打断开始：按住说话和静音都在本地控制上行，通话中随时能切。
  ws.onopen = () => ws.send(JSON.stringify({ type: "start", format: "pcm_s16le", sample_rate: CALL_SAMPLE_RATE, segmentation: "server_vad", barge_in: true }));
  ws.onmessage = (event) => {
    if (typeof event.data === "string") handleMessage(JSON.parse(event.data) as CallServerMessage);
    else collector.push(event.data as ArrayBuffer);
  };
  ws.onclose = (event) => {
    if (socket !== ws) return;
    if (connecting.value) problem.value = event.reason || "没接通：确认已登录，并且语音识别和语音合成都配好了。";
    endCall();
  };
}

function fail(message: string): void {
  problem.value = message;
  endCall();
}

function handleMessage(msg: CallServerMessage): void {
  turns.value = applyCallMessage(turns.value, msg);
  switch (msg.type) {
    case "ready":
      connecting.value = false;
      inCall.value = true;
      if (msg.ready?.bot_name) botName.value = msg.ready.bot_name;
      clock = setInterval(() => elapsed.value++, 1000);
      startCapture();
      break;
    case "state":
      if (msg.state) callState.value = msg.state;
      break;
    case "speech":
      userSpeaking.value = msg.event === "start";
      break;
    case "audio_start":
      collector.start(msg.turn ?? 0, msg.seq ?? 0, msg.mime ?? "audio/mpeg");
      break;
    case "audio_end": {
      const sentence = collector.end(msg.turn ?? 0, msg.seq ?? 0);
      if (sentence) enqueuePlayback(sentence.data);
      break;
    }
    case "interrupted":
      stopPlayback();
      break;
    case "error":
      if (msg.fatal) fail(msg.text || msg.code || "通话出错");
      break;
  }
}

function startCapture(): void {
  if (!micStream) return;
  micSource = audioContext.createMediaStreamSource(micStream);
  // ScriptProcessor 已经不推荐，但各家手机浏览器都支持，不用另外加载 Worklet 文件。
  processor = audioContext.createScriptProcessor(2048, 1, 1);
  const rate = audioContext.sampleRate;
  processor.onaudioprocess = (event) => {
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    // 按住说话时只在按着的时候上行；静音时照常发静音帧，服务端靠它判断说完没有。
    if (pushToTalk.value && !holding.value) return;
    const samples = downsample(event.inputBuffer.getChannelData(0), rate);
    socket.send(floatToPCM16(muted.value ? new Float32Array(samples.length) : samples));
  };
  micSource.connect(processor);
  // 不接到输出上 onaudioprocess 不触发；输出是静音的。
  processor.connect(audioContext.destination);
}

// 一句一句排着放：合成完的句子可能比上一句还没放完的时候先到。
function enqueuePlayback(data: ArrayBuffer): void {
  const epoch = playbackEpoch;
  playbackChain = playbackChain.then(async () => {
    if (epoch !== playbackEpoch || ended.value) return;
    let buffer: AudioBuffer;
    try {
      buffer = await audioContext.decodeAudioData(data);
    } catch {
      return;
    }
    if (epoch !== playbackEpoch || ended.value) return;
    const source = audioContext.createBufferSource();
    source.buffer = buffer;
    source.connect(audioContext.destination);
    const startAt = Math.max(audioContext.currentTime + 0.02, playbackCursor);
    source.start(startAt);
    playbackCursor = startAt + buffer.duration;
    playbackSources.push(source);
    source.onended = () => {
      playbackSources = playbackSources.filter((item) => item !== source);
    };
  });
}

function stopPlayback(): void {
  playbackEpoch++;
  collector.reset();
  for (const source of playbackSources) {
    try {
      source.stop();
    } catch {
      // 已经放完的停不了，忽略。
    }
  }
  playbackSources = [];
  playbackCursor = 0;
}

function send(message: Record<string, unknown>): void {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
}

function interrupt(): void {
  stopPlayback();
  send({ type: "interrupt" });
}

function togglePushToTalk(): void {
  pushToTalk.value = !pushToTalk.value;
  // 切过去时正在说的半句立即送出去，不留在服务端等静音。
  if (pushToTalk.value) send({ type: "commit" });
}

async function toggleTyping(): Promise<void> {
  typing.value = !typing.value;
  if (typing.value) {
    await nextTick();
    typingInput.value?.focus();
  }
}

function pressTalk(): void {
  holding.value = true;
  if (callState.value !== "listening") interrupt();
}

function releaseTalk(): void {
  if (!holding.value) return;
  holding.value = false;
  send({ type: "commit" });
}

function sendText(): void {
  const text = typed.value.trim();
  if (!text) return;
  stopPlayback();
  send({ type: "text", text });
  typed.value = "";
}

function hangup(): void {
  if (ended.value) return emit("close");
  send({ type: "hangup" });
  endCall();
  emit("close");
}

function endCall(): void {
  ended.value = true;
  inCall.value = false;
  connecting.value = false;
  holding.value = false;
  userSpeaking.value = false;
  cleanup();
}

function cleanup(): void {
  if (clock) clearInterval(clock);
  clock = undefined;
  const ws = socket;
  socket = null;
  if (ws && ws.readyState <= WebSocket.OPEN) ws.close();
  stopPlayback();
  processor?.disconnect();
  micSource?.disconnect();
  processor = null;
  micSource = null;
  // 麦克风请求可能还没回来：回来了也要关掉。
  void props.audio.mic.then((stream) => stream.getTracks().forEach((track) => track.stop())).catch(() => undefined);
  micStream = null;
  if (audioContext.state !== "closed") void audioContext.close();
}

onBeforeUnmount(() => {
  if (!ended.value) send({ type: "hangup" });
  ended.value = true;
  cleanup();
});
</script>

<style scoped>
/* 通话界面不跟随亮暗主题：和手机上的通话界面一样，固定深色。 */
.vc-screen {
  --vc-fg: #fff;
  --vc-dim: rgba(255, 255, 255, 0.68);
  --vc-faint: rgba(255, 255, 255, 0.16);
  --vc-red: #f04438;
  position: fixed;
  inset: 0;
  z-index: 300;
  display: grid;
  grid-template-rows: auto auto 1fr auto auto;
  justify-items: center;
  overflow: hidden;
  padding: max(16px, env(safe-area-inset-top)) 16px max(28px, env(safe-area-inset-bottom));
  background: #1d1a22;
  color: var(--vc-fg);
}

.vc-backdrop {
  position: absolute;
  inset: -40px;
  z-index: -1;
  background: radial-gradient(circle at 50% 28%, color-mix(in srgb, var(--accent) 45%, #2a2430), #141218 70%) center / cover;
  filter: blur(36px) brightness(0.5) saturate(1.2);
}

.vc-top {
  display: flex;
  justify-content: center;
  width: 100%;
}

.vc-top-label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: var(--vc-dim);
}

.vc-peer {
  display: grid;
  justify-items: center;
  gap: 6px;
  margin-top: clamp(24px, 8vh, 72px);
}

.vc-avatar-wrap {
  position: relative;
  display: grid;
  place-items: center;
  width: 112px;
  height: 112px;
  margin-bottom: 14px;
}

.vc-avatar {
  position: relative;
  width: 100%;
  height: 100%;
  border-radius: 28px;
  object-fit: cover;
  box-shadow: 0 10px 40px rgba(0, 0, 0, 0.35);
}

.vc-avatar-fallback {
  display: grid;
  place-items: center;
  background: var(--accent);
  font-size: 44px;
  font-weight: 600;
}

.vc-ring {
  position: absolute;
  inset: 0;
  border-radius: 32px;
  border: 2px solid color-mix(in srgb, var(--accent) 70%, #fff);
  opacity: 0;
}

.vc-peer.speaking .vc-ring,
.vc-peer.idle .vc-ring {
  animation: vc-ripple 1.8s ease-out infinite;
}

.vc-peer.speaking .vc-ring.delay,
.vc-peer.idle .vc-ring.delay {
  animation-delay: 0.9s;
}

.vc-peer.user-speaking .vc-avatar {
  transform: scale(0.96);
  transition: transform 0.2s;
}

@keyframes vc-ripple {
  from {
    transform: scale(1);
    opacity: 0.7;
  }
  to {
    transform: scale(1.45);
    opacity: 0;
  }
}

.vc-name {
  margin: 0;
  font-size: 26px;
  font-weight: 600;
  letter-spacing: 0.01em;
}

.vc-status {
  margin: 0;
  font-size: 15px;
  color: var(--vc-dim);
  font-variant-numeric: tabular-nums;
}

.vc-captions {
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
  gap: 10px;
  width: min(100%, 520px);
  padding: 16px 4px;
  overflow: hidden;
  text-align: center;
}

.vc-caption,
.vc-problem {
  margin: 0;
  max-width: 100%;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

.vc-caption.user {
  font-size: 14px;
  color: var(--vc-dim);
}

.vc-caption.bot {
  font-size: 17px;
}

.vc-caption.cut {
  color: var(--vc-dim);
  text-decoration: line-through rgba(255, 255, 255, 0.35);
}

.vc-problem {
  padding: 10px 14px;
  border-radius: 12px;
  background: rgba(240, 68, 56, 0.18);
  font-size: 14px;
}

.vc-interrupt {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border: 0;
  border-radius: 999px;
  background: var(--vc-faint);
  color: var(--vc-fg);
  font-size: 13px;
  cursor: pointer;
}

.vc-typing {
  display: flex;
  gap: 8px;
  width: min(100%, 420px);
  margin-bottom: 18px;
}

.vc-typing input {
  flex: 1;
  min-width: 0;
  height: 42px;
  padding: 0 16px;
  border: 0;
  border-radius: 999px;
  background: var(--vc-faint);
  color: var(--vc-fg);
  font: inherit;
  outline: none;
}

.vc-typing input::placeholder {
  color: var(--vc-dim);
}

.vc-typing button {
  display: grid;
  place-items: center;
  width: 42px;
  height: 42px;
  border: 0;
  border-radius: 50%;
  background: var(--accent);
  color: #fff;
  cursor: pointer;
}

.vc-typing button:disabled {
  opacity: 0.45;
  cursor: default;
}

.vc-controls {
  display: grid;
  gap: 26px;
  width: min(100%, 360px);
}

.vc-row {
  display: flex;
  justify-content: space-between;
}

.vc-row-main {
  justify-content: center;
  gap: 56px;
}

.vc-btn {
  display: grid;
  justify-items: center;
  gap: 8px;
  width: 84px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--vc-fg);
  cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}

.vc-btn:disabled {
  cursor: default;
  opacity: 0.4;
}

.vc-circle {
  display: grid;
  place-items: center;
  width: 64px;
  height: 64px;
  border-radius: 50%;
  background: var(--vc-faint);
  transition: background 0.15s, color 0.15s, transform 0.1s;
}

.vc-btn:not(:disabled):active .vc-circle {
  transform: scale(0.94);
}

.vc-btn.on .vc-circle {
  background: #fff;
  color: #1d1a22;
}

.vc-label {
  font-size: 12px;
  color: var(--vc-dim);
  white-space: nowrap;
}

.vc-hangup .vc-circle,
.vc-ptt .vc-circle {
  width: 72px;
  height: 72px;
}

.vc-hangup .vc-circle {
  background: var(--vc-red);
}

.vc-ptt {
  touch-action: none;
  user-select: none;
  -webkit-user-select: none;
}

.vc-ptt.on .vc-circle {
  background: var(--accent);
  color: #fff;
  transform: scale(1.08);
}

.vc-btn:focus-visible .vc-circle,
.vc-interrupt:focus-visible {
  outline: 2px solid #fff;
  outline-offset: 3px;
}
</style>
