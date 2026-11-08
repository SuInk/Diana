<template>
  <div class="desktop-settings" :aria-busy="busy">
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <section v-if="status?.authorize" class="card">
      <div class="card-header"><h2>开启步骤</h2><span class="badge" :class="stepsDone === steps.length ? 'ok' : ''">{{ stepsDone }}/{{ steps.length }}</span></div>
      <div class="card-body stack">
        <p class="hint">按顺序走完就能让 Diana 操作这台 Mac。也可以直接在聊天里跟 Diana 说「打开电脑操作」，它会帮你打开第 1、5、6 步的开关；系统授权那两步只能你在这台 Mac 上点。</p>
        <ol class="desktop-steps">
          <li v-for="(step, index) in steps" :key="step.key" class="desktop-step" :class="{ done: step.done }">
            <span class="desktop-step-mark" aria-hidden="true"><Check v-if="step.done" :size="14" /><template v-else>{{ index + 1 }}</template></span>
            <div class="desktop-step-body">
              <strong>{{ step.title }}</strong>
              <p class="hint">{{ step.hint }}</p>
              <ul v-if="step.key === 'bot' && bots.length" class="desktop-bots">
                <li v-for="bot in bots" :key="bot.id">
                  <span class="badge" :class="bot.enabled ? 'ok' : ''">{{ bot.enabled ? '已开' : '未开' }}</span>
                  {{ bot.name }}
                  <a v-if="!bot.enabled" class="desktop-link" :href="`/bot?profile=${encodeURIComponent(bot.id)}&tab=advanced`">去打开</a>
                </li>
              </ul>
              <div v-if="!step.done && step.actions.length" class="cluster desktop-actions">
                <button v-for="action in step.actions" :key="action.label" class="btn small" :class="{ primary: action.primary }" type="button" :disabled="busy || action.disabled" @click="action.run">{{ action.label }}</button>
              </div>
            </div>
          </li>
        </ol>
        <p v-if="status.authorize && !status.authorize.bundle" class="hint">这台 Mac 是手动部署的：系统设置列表里要找的是「{{ status.authorize.name }}」，位置 <code>{{ status.authorize.path }}</code>。列表里没有时点「+」，按 ⇧⌘G 粘贴这个路径。</p>
      </div>
    </section>
    <section class="card">
      <div class="card-header desktop-header">
        <span class="badge" :class="takenOver ? 'warn' : status?.ready ? 'ok' : ''">{{ takenOver ? '人工接管中' : status?.ready ? '执行器已连接' : '桌面尚未就绪' }}</span>
        <button class="btn small ghost" type="button" :disabled="busy" aria-label="刷新桌面状态" title="刷新桌面状态" @click="refresh"><RefreshCw :size="14" aria-hidden="true" /></button>
      </div>
      <form v-if="status" class="card-body form-grid" @submit.prevent="save">
        <p v-if="status.detail" class="hint field wide">{{ status.detail }}</p>
        <p v-if="!status.helper_configured" class="hint field wide">桌面执行器目前只支持 macOS 14 或更新版本。</p>
        <div class="field wide desktop-option">
          <div><label for="desktop-enabled">桌面控制</label><p class="hint">允许 Diana 查看已授权应用的窗口。还需在「机器人 → 配置 → 高级」里打开「允许使用桌面控制」。</p></div>
          <label class="switch"><input id="desktop-enabled" v-model="policy.enabled" type="checkbox" aria-label="启用桌面控制" /><span class="track" aria-hidden="true"></span></label>
        </div>
        <div class="field wide desktop-option">
          <div><label for="desktop-write">点击与输入</label><p class="hint">允许 Diana 自己点击、输入、按键和滚动。每步前它会先截图核对画面，想叫停随时点「接管桌面」。</p></div>
          <label class="switch"><input id="desktop-write" v-model="policy.write_enabled" type="checkbox" aria-label="允许点击与输入" /><span class="track" aria-hidden="true"></span></label>
        </div>
        <div class="field"><label for="desktop-allowed">允许的应用</label><input id="desktop-allowed" class="input" v-model="allowed" placeholder="例如 com.apple.TextEdit" /><span class="hint">填写应用名称或 Bundle ID，多项用逗号分隔；留空允许全部应用。</span></div>
        <div class="field"><label for="desktop-denied">禁止的应用</label><input id="desktop-denied" class="input" v-model="denied" placeholder="多项用逗号分隔" /><span class="hint">禁止名单优先于允许名单。</span></div>
        <div class="field wide"><div class="cluster"><button class="btn primary" type="submit" :disabled="busy || !policyDirty"><Save :size="15" aria-hidden="true" />保存权限</button></div></div>
      </form>
      <div v-if="status" class="card-body desktop-system">
        <p v-if="status.executor" class="hint">执行器：{{ status.executor }}</p>
        <div class="desktop-option">
          <div><strong>人工接管</strong><p class="hint">{{ takenOver ? 'Diana 已停止操作，重连和重启后仍保持接管。' : '立即停止 Diana 的桌面操作，由你接手。' }}</p></div>
          <button class="btn small" type="button" :disabled="busy" @click="takeover"><Hand :size="14" aria-hidden="true" />{{ takenOver ? '结束接管' : '接管桌面' }}</button>
        </div>
      </div>
    </section>
    <section class="card">
      <div class="card-header"><h2>电脑任务</h2><span v-if="jobs.length" class="badge">{{ jobs.length }}</span></div>
      <div class="card-body stack">
        <p v-if="!jobs.length" class="hint">暂无电脑任务。启用后，可以让 Diana 创建任务。</p>
        <article v-for="job in jobs" :key="job.id" class="desktop-item">
          <div class="desktop-item-heading"><strong>{{ job.goal || '未命名任务' }}</strong><span class="badge" :class="job.status === 'waiting_confirm' ? 'warn' : job.status === 'succeeded' ? 'ok' : ''">{{ labels[job.status] || job.status }}</span></div>
          <p class="hint">已用 {{ job.budget.steps_used }} 步<template v-if="job.wait_reason || job.error"> · {{ job.wait_reason || job.error }}</template></p>
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
import { controlDesktopJob, getBotProfileConfig, getDesktopJobs, getDesktopStatus, reconnectDesktop, requestDesktopPermission, saveDesktopPolicy, setDesktopTakeover, type DesktopJob, type DesktopPermissionKind, type DesktopPolicy, type DesktopStatus } from "../api";
import { toastSuccess } from "../lib/toast";
const status = ref<DesktopStatus | null>(null);
const policy = ref<DesktopPolicy>({ enabled: false, write_enabled: false });
const allowed = ref("");
const denied = ref("");
const jobs = ref<DesktopJob[]>([]);
const busy = ref(false);
const error = ref("");
const labels: Record<string, string> = { queued: "等待执行", running: "执行中", paused: "已暂停", waiting_confirm: "等待确认", succeeded: "已完成", failed: "失败", cancelled: "已取消" };
const policyDirty = computed(() => !!status.value && JSON.stringify({ ...policy.value, allowed_apps: split(allowed.value), denied_apps: split(denied.value) }) !== JSON.stringify({ ...status.value.policy, allowed_apps: status.value.policy.allowed_apps || [], denied_apps: status.value.policy.denied_apps || [] }));
const bots = ref<{ id: string; name: string; enabled: boolean }[]>([]);
type StepAction = { label: string; run: () => void; primary?: boolean; disabled?: boolean };
type Step = { key: string; title: string; hint: string; done: boolean; actions: StepAction[] };
const appName = computed(() => status.value?.authorize?.name || "Diana");
const enablePolicy = (patch: Partial<DesktopPolicy>) => run(async () => { await saveDesktopPolicy({ ...status.value!.policy, ...patch }); await load(true); });
const askPermission = (kind: DesktopPermissionKind) => run(async () => {
  const res = await requestDesktopPermission(kind);
  await load();
  toastSuccess(res.opened ? `已在这台 Mac 上打开系统设置，把「${res.authorize?.name || appName.value}」的开关打开` : `到这台 Mac 的系统设置里打开「${res.authorize?.name || appName.value}」的开关`);
});
const recheck = () => run(async () => { await reconnectDesktop(); await load(); });
const steps = computed<Step[]>(() => {
  const s = status.value;
  if (!s) return [];
  const enabled = s.policy.enabled;
  const ready = enabled && s.ready;
  const perm = s.permissions;
  const name = appName.value;
  const permissionStep = (key: DesktopPermissionKind, title: string, what: string): Step => ({
    key, title, done: !!perm?.[key],
    hint: !ready ? `${what}。前两步完成后再来授权。` : `${what}。点「请求授权」会在这台 Mac 上弹出系统设置，打开「${name}」的开关；打开后点「重新检测」。`,
    actions: ready ? [{ label: "请求授权", run: () => askPermission(key), primary: true }, { label: "重新检测", run: recheck }] : [],
  });
  return [
    { key: "enabled", title: "打开桌面控制", done: enabled, hint: "总开关，默认关。", actions: [{ label: "打开", run: () => enablePolicy({ enabled: true }), primary: true }] },
    { key: "executor", title: "准备执行器", done: ready, hint: !enabled ? "打开总开关后自动下载执行器 cua-driver，一般一两分钟。" : s.detail || "正在连接执行器…", actions: enabled ? [{ label: "重新检测", run: recheck }] : [] },
    permissionStep("screen_recording", "允许屏幕录制", "让 Diana 看得到窗口"),
    permissionStep("accessibility", "允许辅助功能", "让 Diana 能点击、输入和按键"),
    { key: "write", title: "允许点击与输入", done: s.policy.write_enabled, hint: "只想让 Diana 看看窗口可以跳过这一步。", actions: [{ label: "打开", run: () => enablePolicy({ write_enabled: true }), primary: true, disabled: !enabled }] },
    { key: "bot", title: "给机器人打开桌面控制", done: bots.value.some(b => b.enabled), hint: "在机器人配置「高级」页签里打开「允许使用桌面控制」，只对主人生效。", actions: [] },
  ];
});
const stepsDone = computed(() => steps.value.filter(step => step.done).length);
const takenOver = computed(() => status.value?.takeover || status.value?.connections.some(c => c.takeover));
async function load(reset = false) {
  const [s, j, cfg] = await Promise.all([getDesktopStatus(), getDesktopJobs(), getBotProfileConfig().catch(() => null)]);
  if (cfg) bots.value = (cfg.profiles?.length ? cfg.profiles : [cfg]).map(p => ({ id: p.id || "", name: p.name || p.id || "机器人", enabled: !!p.agent_desktop_control_enabled }));
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
.desktop-steps { list-style: none; margin: 0; padding: 0; display: grid; gap: 14px; }
.desktop-step { display: flex; gap: 12px; align-items: flex-start; min-width: 0; }
.desktop-step-mark { flex-shrink: 0; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; font-size: 12px; border: 1px solid var(--border); color: var(--muted); }
.desktop-step.done .desktop-step-mark { background: var(--accent, var(--primary)); border-color: transparent; color: #fff; }
.desktop-step.done .desktop-step-body strong { color: var(--muted); }
.desktop-step-body { display: grid; gap: 6px; min-width: 0; }
.desktop-step-body strong { font-size: 13px; font-weight: 500; }
.desktop-bots { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; font-size: 13px; }
.desktop-bots li { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.desktop-link { font-size: 12.5px; }
code { overflow-wrap: anywhere; }
@media (max-width: 520px) { .desktop-option { align-items: flex-start; } }
</style>
