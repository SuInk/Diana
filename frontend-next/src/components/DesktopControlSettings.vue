<template>
  <div class="settings-section-body desktop-settings" :aria-busy="busy">
    <div class="desktop-toolbar">
      <strong>{{ status?.ready ? '本机 helper 已连接' : '桌面尚未就绪' }}</strong>
      <button class="btn" :disabled="busy" @click="refresh">刷新</button>
    </div>
    <p v-if="status?.detail" class="muted hint">{{ status.detail }}</p>
    <p v-if="status && !status.helper_configured" class="muted hint">在 macOS 主机安装桌面 helper，并通过 DIANA_DESKTOP_HELPER 配置其绝对路径后重启 Diana。需要 macOS 14 或更新版本。</p>
    <p v-if="error" role="alert" class="desktop-error">{{ error }}</p>
    <form v-if="status" @submit.prevent="save">
      <label class="desktop-check"><input v-model="policy.enabled" type="checkbox" /> 启用桌面控制（读取窗口和截图）</label>
      <label class="desktop-check"><input v-model="policy.write_enabled" type="checkbox" /> 允许点击、输入文字和按键</label>
      <p class="muted hint">还需在机器人配置中启用桌面工具。macOS 的屏幕录制与辅助功能权限需在系统设置中授权。</p>
      <label class="desktop-field">允许的应用<input class="input" v-model="allowed" placeholder="com.apple.TextEdit，多项用逗号分隔" /></label>
      <p class="muted hint">留空允许全部应用。填写应用 Bundle ID 或应用名称。</p>
      <label class="desktop-field">禁止的应用<input class="input" v-model="denied" placeholder="禁止名单优先于允许名单" /></label>
      <button class="btn primary" type="submit" :disabled="busy">保存权限</button>
    </form>
    <div v-if="status?.connections.length" class="desktop-toolbar">
      <span>{{ takenOver ? '人工接管中，模型操作已暂停' : '模型可以按已保存的权限操作' }}</span>
      <button class="btn" :disabled="busy" @click="takeover">{{ takenOver ? '结束接管' : '人工接管' }}</button>
    </div>
    <h3>电脑任务</h3>
    <p v-if="!jobs.length" class="muted hint">暂无电脑任务。启用后，可让 Diana 创建并执行任务。</p>
    <article v-for="job in jobs" :key="job.id" class="desktop-job">
      <div><strong>{{ job.goal || '未命名任务' }}</strong><p>{{ labels[job.status] || job.status }} · {{ job.budget.steps_used }} / {{ job.budget.max_steps }} 步</p></div>
      <p v-if="job.wait_reason || job.error" class="muted hint">{{ job.wait_reason || job.error }}</p>
      <div class="desktop-actions">
        <button v-if="job.status === 'running'" class="btn" :disabled="busy" @click="act(job, 'pause')">暂停</button>
        <button v-if="job.status === 'paused'" class="btn" :disabled="busy" @click="act(job, 'resume')">恢复</button>
        <button v-if="job.status === 'waiting_confirm'" class="btn primary" :disabled="busy" @click="act(job, 'confirm')">确认继续</button>
        <button v-if="!['succeeded', 'failed', 'cancelled'].includes(job.status)" class="btn" :disabled="busy" @click="act(job, 'cancel')">取消任务</button>
      </div>
    </article>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { controlDesktopJob, getDesktopJobs, getDesktopStatus, saveDesktopPolicy, setDesktopTakeover, type DesktopJob, type DesktopPolicy, type DesktopStatus } from "../api";
import { toastSuccess } from "../toast";
const status = ref<DesktopStatus | null>(null);
const policy = ref<DesktopPolicy>({ enabled: false, write_enabled: false });
const allowed = ref("");
const denied = ref("");
const jobs = ref<DesktopJob[]>([]);
const busy = ref(false);
const error = ref("");
const labels: Record<string, string> = { queued: "等待执行", running: "执行中", paused: "已暂停", waiting_confirm: "等待确认", succeeded: "已完成", failed: "失败", cancelled: "已取消" };
const takenOver = computed(() => status.value?.connections.some(c => c.takeover));
async function load(reset = false) {
  const [s, j] = await Promise.all([getDesktopStatus(), getDesktopJobs()]);
  status.value = s;
  jobs.value = j.jobs || [];
  if (reset) {
    policy.value = { ...s.policy };
    allowed.value = (s.policy.allowed_apps || []).join(", ");
    denied.value = (s.policy.denied_apps || []).join(", ");
  }
}
async function run(fn: () => Promise<unknown>) {
  busy.value = true;
  error.value = "";
  try { await fn(); } catch (e) { error.value = e instanceof Error ? e.message : String(e); }
  finally { busy.value = false; }
}
const refresh = () => run(() => load(!status.value));
const split = (s: string) => s.split(/[,，\n]/).map(v => v.trim()).filter(Boolean);
const save = () => run(async () => { await saveDesktopPolicy({ ...policy.value, allowed_apps: split(allowed.value), denied_apps: split(denied.value) }); await load(true); toastSuccess("桌面权限已保存"); });
const takeover = () => run(async () => { await setDesktopTakeover(!takenOver.value); await load(); });
const act = (job: DesktopJob, action: "pause" | "resume" | "confirm" | "cancel") => run(async () => { await controlDesktopJob(job.id, action); await load(); });
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => { void refresh(); timer = setInterval(() => { if (!busy.value) void refresh(); }, 5000); });
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>
<style scoped>
.hint { font-size: 13px; line-height: 1.6; margin: 0; }
.desktop-settings { display: grid; gap: 1rem; max-width: 760px; }
.desktop-toolbar, .desktop-actions { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
.desktop-actions { justify-content: flex-start; }
.desktop-check { display: flex; align-items: center; gap: .6rem; margin-bottom: 1rem; }
.desktop-field { display: grid; gap: .5rem; margin: 1rem 0; }
.desktop-field input { width: 100%; }
.desktop-job { border: 1px solid var(--line); border-radius: 12px; padding: 1rem; }
.desktop-error { color: var(--danger, #b42318); }
</style>
