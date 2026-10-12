<template>
  <div class="desktop-settings" :aria-busy="busy">
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <section class="card">
      <div class="card-header desktop-header">
        <span class="badge" :class="takenOver ? 'warn' : status?.ready ? 'ok' : ''">{{ takenOver ? '人工接管中' : status?.ready ? '执行器已连接' : '桌面尚未就绪' }}</span>
        <button class="btn small ghost" type="button" :disabled="busy" aria-label="刷新桌面状态" title="刷新桌面状态" @click="refresh"><RefreshCw :size="14" aria-hidden="true" /></button>
      </div>
      <form v-if="status" class="card-body form-grid" @submit.prevent="save">
        <p v-if="status.detail" class="hint field wide">{{ status.detail }}</p>
        <p v-if="!status.helper_configured" class="hint field wide">请在 macOS 主机安装桌面执行器后重启 Diana。需要 macOS 14 或更新版本。</p>
        <div class="field wide desktop-option">
          <div><label for="desktop-enabled">桌面控制</label><p class="hint">允许 Diana 查看已授权应用的窗口。还需在机器人配置中启用桌面工具。</p></div>
          <label class="switch"><input id="desktop-enabled" v-model="policy.enabled" type="checkbox" aria-label="启用桌面控制" /><span class="track" aria-hidden="true"></span></label>
        </div>
        <div class="field wide desktop-option">
          <div><label for="desktop-write">点击与输入</label><p class="hint">允许点击、输入、按键和滚动，每条操作执行前需在下方确认。</p></div>
          <label class="switch"><input id="desktop-write" v-model="policy.write_enabled" type="checkbox" aria-label="允许点击与输入" /><span class="track" aria-hidden="true"></span></label>
        </div>
        <div class="field"><label for="desktop-allowed">允许的应用</label><input id="desktop-allowed" class="input" v-model="allowed" placeholder="例如 com.apple.TextEdit" /><span class="hint">填写应用名称或 Bundle ID，多项用逗号分隔；留空允许全部应用。</span></div>
        <div class="field"><label for="desktop-denied">禁止的应用</label><input id="desktop-denied" class="input" v-model="denied" placeholder="多项用逗号分隔" /><span class="hint">禁止名单优先于允许名单。</span></div>
        <div class="field wide"><div class="cluster"><button class="btn primary" type="submit" :disabled="busy || !policyDirty"><Save :size="15" aria-hidden="true" />保存权限</button></div></div>
      </form>
      <div v-if="status" class="card-body desktop-system">
        <div v-if="status.permissions" class="cluster desktop-permissions">
          <span class="badge" :class="status.permissions.screen_recording ? 'ok' : 'warn'">屏幕录制{{ status.permissions.screen_recording ? '已授权' : '未授权' }}</span>
          <span class="badge" :class="status.permissions.accessibility ? 'ok' : 'warn'">辅助功能{{ status.permissions.accessibility ? '已授权' : '未授权' }}</span>
        </div>
        <p class="hint">系统权限需在 macOS「隐私与安全性」中授权。</p>
        <div class="desktop-option">
          <div><strong>人工接管</strong><p class="hint">{{ takenOver ? 'Diana 已停止操作，重连和重启后仍保持接管。' : '立即停止 Diana 的桌面操作，由你接手。' }}</p></div>
          <button class="btn small" type="button" :disabled="busy" @click="takeover"><Hand :size="14" aria-hidden="true" />{{ takenOver ? '结束接管' : '接管桌面' }}</button>
        </div>
      </div>
    </section>
    <section v-if="status?.pending_actions?.length" class="card">
      <div class="card-header"><h2>待确认操作</h2><span class="badge">{{ status.pending_actions.length }}</span></div>
      <div class="card-body stack">
        <p class="hint">确认仅对本次操作有效，两分钟后过期。画面变化需重新观察；确认后让 Diana 继续。</p>
        <article v-for="action in status.pending_actions" :key="action.id" class="desktop-item">
          <div class="desktop-item-heading"><strong>{{ actionLabels[action.command.op] || '桌面操作' }}</strong><span v-if="action.approved" class="badge ok">已确认</span></div>
          <p class="hint">{{ action.command.expected_bundle_id || '目标应用' }} · 窗口 {{ action.command.window_id }}</p>
          <img v-if="action.preview?.data" class="desktop-observation" :src="`data:image/png;base64,${action.preview.data}`" alt="请求操作时的目标窗口画面" />
          <pre v-if="action.command.text" class="desktop-preview">{{ action.command.text }}</pre>
          <p v-if="action.command.key" class="desktop-detail">按键：{{ action.command.key }}</p>
          <p v-if="action.command.element_id" class="desktop-detail">目标元素：{{ action.target_label || action.command.element_id }}</p>
          <p v-else-if="action.command.x !== undefined" class="desktop-detail">窗口内位置：{{ action.command.x }}, {{ action.command.y }}</p>
          <p v-if="action.command.op === 'window.scroll'" class="desktop-detail">滚动：水平 {{ action.command.delta_x || 0 }}，垂直 {{ action.command.delta_y || 0 }} 像素</p>
          <div class="cluster"><button class="btn small primary" type="button" :disabled="busy || takenOver || action.approved || !status.policy.write_enabled" @click="approve(action.id)"><Check :size="14" aria-hidden="true" />{{ action.approved ? '等待执行' : '确认这条操作' }}</button></div>
        </article>
      </div>
    </section>
    <section class="card">
      <div class="card-header"><h2>电脑任务</h2><span v-if="jobs.length" class="badge">{{ jobs.length }}</span></div>
      <div class="card-body stack">
        <p v-if="!jobs.length" class="hint">暂无电脑任务。启用后，可以让 Diana 创建任务。</p>
        <article v-for="job in jobs" :key="job.id" class="desktop-item">
          <div class="desktop-item-heading"><strong>{{ job.goal || '未命名任务' }}</strong><span class="badge" :class="job.status === 'waiting_confirm' ? 'warn' : job.status === 'succeeded' ? 'ok' : ''">{{ labels[job.status] || job.status }}</span></div>
          <p class="hint">已用 {{ job.budget.steps_used }} / {{ job.budget.max_steps }} 步<template v-if="job.wait_reason || job.error"> · {{ job.wait_reason || job.error }}</template></p>
          <div class="cluster desktop-actions">
            <button v-if="job.status === 'running'" class="btn small" type="button" :disabled="busy" @click="act(job, 'pause')">暂停</button>
            <button v-if="job.status === 'paused'" class="btn small" type="button" :disabled="busy" @click="act(job, 'resume')">恢复</button>
            <button v-if="job.status === 'waiting_confirm'" class="btn small primary" type="button" :disabled="busy" @click="act(job, 'confirm')">继续任务</button>
            <button v-if="!['succeeded', 'failed', 'cancelled'].includes(job.status)" class="btn small ghost" type="button" :disabled="busy" @click="act(job, 'cancel')">取消任务</button>
          </div>
        </article>
      </div>
    </section>
  </div>
</template>
<script setup lang="ts">
import { RefreshCw, Save, Hand, Check } from "@lucide/vue";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { confirmDesktopAction, controlDesktopJob, getDesktopJobs, getDesktopStatus, saveDesktopPolicy, setDesktopTakeover, type DesktopJob, type DesktopPolicy, type DesktopStatus } from "../api";
import { toastSuccess } from "../toast";
const status = ref<DesktopStatus | null>(null);
const policy = ref<DesktopPolicy>({ enabled: false, write_enabled: false });
const allowed = ref("");
const denied = ref("");
const jobs = ref<DesktopJob[]>([]);
const busy = ref(false);
const error = ref("");
const labels: Record<string, string> = { queued: "等待执行", running: "执行中", paused: "已暂停", waiting_confirm: "等待确认", succeeded: "已完成", failed: "失败", cancelled: "已取消" };
const actionLabels: Record<string, string> = { "window.type": "输入文字", "window.click": "点击", "window.key": "按键", "window.scroll": "滚动" };
const policyDirty = computed(() => !!status.value && JSON.stringify({ ...policy.value, allowed_apps: split(allowed.value), denied_apps: split(denied.value) }) !== JSON.stringify({ ...status.value.policy, allowed_apps: status.value.policy.allowed_apps || [], denied_apps: status.value.policy.denied_apps || [] }));
const takenOver = computed(() => status.value?.takeover || status.value?.connections.some(c => c.takeover));
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
const approve = (id: string) => run(async () => { await confirmDesktopAction(id); await load(); });
const act = (job: DesktopJob, action: "pause" | "resume" | "confirm" | "cancel") => run(async () => { await controlDesktopJob(job.id, action); await load(); });
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => { void refresh(); timer = setInterval(() => { if (!busy.value && !document.hidden) void load().catch(() => {}); }, 5000); });
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>
<style scoped>
.desktop-settings { display: grid; gap: 16px; min-width: 0; }
.desktop-header, .desktop-item-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.desktop-option { display: flex; flex-direction: row; align-items: center; justify-content: space-between; gap: 16px; }
.desktop-option > div { min-width: 0; display: grid; gap: 4px; }
.desktop-option label, .desktop-option strong, .desktop-item-heading strong { font-size: 13px; font-weight: 500; }
.desktop-option .switch, .desktop-option .btn { flex-shrink: 0; }
.hint { margin: 0; color: var(--muted); font-size: 12.5px; line-height: 1.6; }
.desktop-system { display: grid; gap: 12px; border-top: 1px solid var(--border); }
.desktop-permissions, .desktop-actions { gap: 8px; }
.desktop-item { display: grid; gap: 8px; min-width: 0; }
.desktop-item + .desktop-item { padding-top: 16px; border-top: 1px solid var(--border); }
.desktop-item-heading { flex-wrap: wrap; }
.desktop-preview { margin: 0; padding: 12px; border: 1px solid var(--border); background: var(--surface-2); border-radius: var(--radius-sm, 8px); font-family: inherit; font-size: 13px; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 180px; overflow: auto; }
.desktop-observation { display: block; max-width: 100%; max-height: 240px; object-fit: contain; border: 1px solid var(--border); border-radius: 8px; }
.desktop-detail { margin: 0; font-size: 13px; }
@media (max-width: 520px) { .desktop-option { align-items: flex-start; } }
</style>
