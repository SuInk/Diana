<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="call-view">
    <section class="card call-card" aria-label="语音通话">
      <header class="call-head">
        <div class="call-bot">
          <h2>{{ info?.bot_name || "语音通话" }}</h2>
          <p class="muted">通话按主人的私聊处理：人设、记忆和私聊记录都是同一份。</p>
        </div>
        <AppSelect v-if="profileOptions.length > 1 && !inCall" v-model="selectedProfile" :options="profileOptions" aria-label="通话的机器人" />
      </header>

      <div v-if="loadError" class="call-banner err" role="alert">{{ loadError }}</div>
      <div v-else-if="info?.problem" class="call-banner warn" role="status">
        {{ info.problem }}。到「提供商」页的模型分配里设置后再来。
      </div>
      <div v-if="!secureContext" class="call-banner warn" role="status">
        浏览器只在 HTTPS 或本机地址下允许使用麦克风，当前地址拿不到录音。
      </div>
      <div v-if="callError" class="call-banner err" role="alert">{{ callError }}</div>

      <div class="call-stage" :class="[stageState, { 'user-speaking': userSpeaking }]">
        <div class="call-orb" aria-hidden="true"><component :is="stageIcon" :size="34" /></div>
        <p class="call-status" role="status" aria-live="polite">{{ stageLabel }}</p>
      </div>

      <div class="call-controls">
        <template v-if="!inCall">
          <button class="btn primary call-main-btn" type="button" :disabled="!canCall || connecting" @click="startCall">
            <Phone :size="18" aria-hidden="true" />{{ connecting ? "正在接通…" : "开始通话" }}
          </button>
        </template>
        <template v-else>
          <button
            v-if="pushToTalk"
            class="btn call-main-btn call-ptt"
            :class="{ active: holding }"
            type="button"
            @pointerdown.prevent="pressTalk"
            @pointerup.prevent="releaseTalk"
            @pointercancel.prevent="releaseTalk"
            @pointerleave="holding && releaseTalk()"
          >
            <Mic :size="18" aria-hidden="true" />{{ holding ? "松开发送" : "按住说话" }}
          </button>
          <button class="btn ghost" type="button" :disabled="callState !== 'speaking' && callState !== 'thinking'" @click="interrupt">
            <Hand :size="16" aria-hidden="true" />打断
          </button>
          <button class="btn danger" type="button" @click="hangup"><PhoneOff :size="16" aria-hidden="true" />挂断</button>
        </template>
      </div>

      <div class="call-options">
        <AppSwitch v-model="pushToTalk" :disabled="inCall">按住说话（关掉时自动检测说完）</AppSwitch>
        <AppSwitch v-model="bargeIn" :disabled="inCall">开口即打断播报</AppSwitch>
      </div>

      <form v-if="inCall" class="call-text" @submit.prevent="sendText">
        <input v-model="typed" class="input" type="text" placeholder="也可以打字说" aria-label="打字说" />
        <button class="btn ghost" type="submit" :disabled="!typed.trim()">发送</button>
      </form>
    </section>

    <section v-if="turns.length" class="card call-log" aria-label="通话记录" aria-live="polite">
      <article v-for="item in turns" :key="item.turn" class="call-turn">
        <p v-if="item.user" class="call-line user">{{ item.user }}</p>
        <p v-if="item.bot.length" class="call-line bot">{{ item.bot.join("") }}</p>
        <span v-if="item.status === 'interrupted'" class="badge warn">已打断</span>
        <span v-else-if="item.status === 'failed'" class="badge err">失败</span>
      </article>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Hand, LoaderCircle, Mic, Phone, PhoneOff, Volume2 } from "@lucide/vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import AppSwitch from "../components/AppSwitch.vue";
import { getBotProfileConfig, getVoiceCallInfo } from "../api";
import { botScope } from "../lib/bot-scope";
import {
  applyCallMessage,
  CALL_SAMPLE_RATE,
  callSocketURL,
  downsample,
  floatToPCM16,
  SentenceAudioCollector,
  type CallInfo,
  type CallServerMessage,
  type CallState,
  type CallTurnView
} from "../lib/voice-call";

const profileOptions = ref<AppSelectOption[]>([]);
const selectedProfile = ref(botScope.value);
const info = ref<CallInfo | null>(null);
const loadError = ref("");
const callError = ref("");
const connecting = ref(false);
const inCall = ref(false);
const callState = ref<CallState>("listening");
const userSpeaking = ref(false);
const holding = ref(false);
const pushToTalk = ref(false);
const bargeIn = ref(true);
const typed = ref("");
const turns = ref<CallTurnView[]>([]);
const secureContext = typeof window === "undefined" || window.isSecureContext;

const canCall = computed(() => !!info.value && !info.value.problem && secureContext);
const stageState = computed(() => (inCall.value ? callState.value : "idle"));
const stageIcon = computed(() => (callState.value === "thinking" && inCall.value ? LoaderCircle : callState.value === "speaking" && inCall.value ? Volume2 : Mic));
const stageLabel = computed(() => {
  if (!inCall.value) return connecting.value ? "正在接通…" : "未在通话";
  if (userSpeaking.value || holding.value) return "在听你说…";
  if (callState.value === "thinking") return "在想…";
  if (callState.value === "speaking") return "在说话";
  return pushToTalk.value ? "按住按钮说话" : "直接说话就行";
});

async function loadProfiles(): Promise<void> {
  try {
    const config = await getBotProfileConfig();
    profileOptions.value = (config.profiles ?? []).filter((profile) => profile.id).map((profile) => ({ value: profile.id!, label: profile.name || profile.id! }));
    if (!selectedProfile.value && profileOptions.value.length) selectedProfile.value = profileOptions.value[0].value;
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err);
  }
}

async function loadInfo(): Promise<void> {
  loadError.value = "";
  try {
    info.value = await getVoiceCallInfo(selectedProfile.value);
  } catch (err) {
    info.value = null;
    loadError.value = err instanceof Error ? err.message : String(err);
  }
}

watch(botScope, (scope) => {
  if (scope && !inCall.value) selectedProfile.value = scope;
});
watch(selectedProfile, () => void loadInfo());
void loadProfiles().then(loadInfo);

// ---- 通话 ----------------------------------------------------------------

let socket: WebSocket | null = null;
let audioContext: AudioContext | null = null;
let micStream: MediaStream | null = null;
let micSource: MediaStreamAudioSourceNode | null = null;
let processor: ScriptProcessorNode | null = null;
let playbackSources: AudioBufferSourceNode[] = [];
let playbackCursor = 0;
let playbackChain: Promise<void> = Promise.resolve();
let playbackEpoch = 0;
const collector = new SentenceAudioCollector();

async function startCall(): Promise<void> {
  callError.value = "";
  connecting.value = true;
  try {
    // 必须在点击里建 AudioContext 并拿麦克风，手机浏览器不允许事后补。
    audioContext = new AudioContext();
    await audioContext.resume();
    micStream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 } });
  } catch (err) {
    connecting.value = false;
    callError.value = `拿不到麦克风：${err instanceof Error ? err.message : String(err)}`;
    cleanup();
    return;
  }
  turns.value = [];
  const ws = new WebSocket(callSocketURL(selectedProfile.value));
  ws.binaryType = "arraybuffer";
  socket = ws;
  ws.onopen = () => {
    ws.send(JSON.stringify({ type: "start", format: "pcm_s16le", sample_rate: CALL_SAMPLE_RATE, segmentation: pushToTalk.value ? "client" : "server_vad", barge_in: bargeIn.value }));
  };
  ws.onmessage = (event) => {
    if (typeof event.data === "string") {
      handleMessage(JSON.parse(event.data) as CallServerMessage);
    } else {
      collector.push(event.data as ArrayBuffer);
    }
  };
  ws.onclose = (event) => {
    if (socket !== ws) return;
    if (connecting.value) callError.value = event.reason || "没接通：确认已登录，并且语音识别和语音合成都配好了";
    cleanup();
  };
  ws.onerror = () => {
    if (connecting.value) callError.value = "没接通";
  };
}

function handleMessage(msg: CallServerMessage): void {
  turns.value = applyCallMessage(turns.value, msg);
  switch (msg.type) {
    case "ready":
      connecting.value = false;
      inCall.value = true;
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
      callError.value = msg.text || msg.code || "通话出错";
      if (msg.fatal) hangup();
      break;
  }
}

function startCapture(): void {
  if (!audioContext || !micStream) return;
  micSource = audioContext.createMediaStreamSource(micStream);
  // ScriptProcessor 已经不推荐，但各家手机浏览器都支持，不用另外加载 Worklet 文件。
  processor = audioContext.createScriptProcessor(2048, 1, 1);
  const rate = audioContext.sampleRate;
  processor.onaudioprocess = (event) => {
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    if (pushToTalk.value && !holding.value) return;
    socket.send(floatToPCM16(downsample(event.inputBuffer.getChannelData(0), rate)));
  };
  micSource.connect(processor);
  // 不接到输出上 onaudioprocess 不触发；输出是静音的。
  processor.connect(audioContext.destination);
}

// 一句一句排着放：合成完的句子可能比上一句还没放完的时候先到。
function enqueuePlayback(data: ArrayBuffer): void {
  const epoch = playbackEpoch;
  playbackChain = playbackChain.then(async () => {
    if (!audioContext || epoch !== playbackEpoch) return;
    let buffer: AudioBuffer;
    try {
      buffer = await audioContext.decodeAudioData(data);
    } catch {
      callError.value = "有一句语音解码失败，浏览器可能不支持这个音频格式";
      return;
    }
    if (epoch !== playbackEpoch) return;
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
  send({ type: "hangup" });
  cleanup();
}

function cleanup(): void {
  const ws = socket;
  socket = null;
  if (ws && ws.readyState <= WebSocket.OPEN) ws.close();
  stopPlayback();
  processor?.disconnect();
  micSource?.disconnect();
  micStream?.getTracks().forEach((track) => track.stop());
  void audioContext?.close();
  processor = null;
  micSource = null;
  micStream = null;
  audioContext = null;
  inCall.value = false;
  connecting.value = false;
  holding.value = false;
  userSpeaking.value = false;
  callState.value = "listening";
}

onBeforeUnmount(cleanup);
</script>

<style scoped>
.call-view {
  display: grid;
  gap: 16px;
  max-width: 640px;
  margin: 0 auto;
}

.call-card {
  display: grid;
  gap: 16px;
  padding: 20px;
}

.call-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.call-head h2 {
  margin: 0 0 4px;
  font-size: 18px;
}

.call-head p {
  margin: 0;
  font-size: 13px;
}

.call-banner {
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  font-size: 13px;
}

.call-banner.warn {
  background: var(--warn-soft);
  color: var(--warn);
}

.call-banner.err {
  background: var(--err-soft);
  color: var(--err);
}

.call-stage {
  display: grid;
  justify-items: center;
  gap: 12px;
  padding: 24px 0 8px;
}

.call-orb {
  display: grid;
  place-items: center;
  width: 112px;
  height: 112px;
  border-radius: 50%;
  background: var(--surface-2);
  color: var(--muted);
  transition: background 0.2s, color 0.2s, transform 0.2s;
}

.call-stage.listening .call-orb {
  background: var(--accent-soft);
  color: var(--accent);
}

.call-stage.user-speaking .call-orb {
  transform: scale(1.08);
  background: var(--accent);
  color: var(--accent-contrast);
}

.call-stage.thinking .call-orb :deep(svg) {
  animation: call-spin 1s linear infinite;
}

.call-stage.speaking .call-orb {
  background: var(--ok-soft);
  color: var(--ok);
  animation: call-pulse 1.2s ease-in-out infinite;
}

@keyframes call-spin {
  to {
    transform: rotate(360deg);
  }
}

@keyframes call-pulse {
  50% {
    transform: scale(1.05);
  }
}

.call-status {
  margin: 0;
  color: var(--text-secondary);
}

.call-controls {
  display: flex;
  justify-content: center;
  gap: 10px;
  flex-wrap: wrap;
}

.call-main-btn {
  min-width: 160px;
  min-height: 44px;
  gap: 8px;
}

.call-ptt {
  touch-action: none;
  user-select: none;
  -webkit-user-select: none;
}

.call-ptt.active {
  background: var(--accent);
  color: var(--accent-contrast);
}

.call-options {
  display: grid;
  gap: 4px;
}

.call-text {
  display: flex;
  gap: 8px;
}

.call-text .input {
  flex: 1;
  min-width: 0;
}

.call-log {
  display: grid;
  gap: 12px;
  padding: 16px 20px;
}

.call-turn {
  display: grid;
  gap: 6px;
}

.call-line {
  margin: 0;
  padding: 8px 12px;
  border-radius: var(--radius-md);
  max-width: 85%;
  white-space: pre-wrap;
  word-break: break-word;
}

.call-line.user {
  justify-self: end;
  background: var(--accent-soft);
}

.call-line.bot {
  justify-self: start;
  background: var(--surface-2);
}

.call-turn .badge {
  justify-self: start;
}
</style>
