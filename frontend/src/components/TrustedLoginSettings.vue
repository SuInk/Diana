<template>
  <div class="stack trusted-login-settings">
    <section class="card">
      <div class="card-header"><div><h2>第三方授权</h2><span class="card-sub">允许其他应用通过 Diana 私聊确认身份。只有指定机器人的主人可以确认登录。</span></div></div>
      <div class="card-body stack">
        <LoadingSkeleton v-if="loading" kind="form" :count="2" label="正在加载第三方授权设置" />
        <div v-else-if="error" class="operation-error" role="alert">{{ error }} <button class="btn small" @click="load">重试</button></div>
        <template v-else-if="settings">
          <AppSwitch :model-value="settings.enabled" :disabled="busy" @update:model-value="toggle">允许第三方授权</AppSwitch>
          <p class="hint">关闭后停止新的登录确认，已登录的会话需要在对应应用中退出或撤销。</p>
        </template>
      </div>
    </section>
    <section v-if="settings && !error" class="card">
      <div class="card-header"><h2>已接入的应用</h2><button class="btn small primary" :disabled="busy || !settings.enabled" @click="openEditor()"><Plus :size="14" />添加应用</button></div>
      <div class="card-body stack">
        <p v-if="!settings.enabled" class="muted">第三方授权已关闭。开启后可以添加应用，现有配置会保留。</p>
        <div v-if="settings.apps.length" class="row-list">
          <div v-for="app in settings.apps" :key="app.id" class="row-item">
            <div class="row-main"><div class="row-title">{{ app.name }} <span class="badge" :class="settings.enabled && !app.disabled ? 'ok' : ''">{{ !settings.enabled ? '总开关已关闭' : app.disabled ? '已停用' : '已启用' }}</span></div>
              <div class="row-sub">确认机器人：{{ profileName(app.profile_id) }} · {{ app.id }}</div>
              <div class="row-sub">最近登录：{{ app.last_login_at ? formatTime(app.last_login_at) : '暂无记录' }}</div>
              <p v-if="profileReason(app.profile_id)" class="hint">{{ profileReason(app.profile_id) }} · <a href="/bot">前往机器人设置</a></p>
            </div><button class="btn small" :disabled="busy" :aria-label="`管理 ${app.name}`" @click="openEditor(app)">管理</button>
          </div>
        </div>
        <p v-else class="muted">还没有接入应用。添加后，Diana 会生成专属连接密钥。</p>
        <p v-if="settings.enabled && !settings.profiles.some(p => p.available)" class="hint">暂无可用的确认机器人。请先在<a href="/bot">机器人设置</a>中配置主人并开启「管理员快速登录」，然后<button class="btn small ghost" @click="load">刷新</button>。</p>
      </div>
    </section>
    <section v-if="settings && !error" class="card">
      <div class="card-header"><h2>最近登录记录</h2><button class="btn small" :disabled="busy" @click="load">刷新</button></div>
      <div class="card-body stack"><p v-if="!settings.records?.length" class="muted">暂无记录。应用成功领取确认结果后会显示在这里。</p>
        <div v-for="(record,index) in [...(settings.records || [])].reverse()" :key="index" class="login-record"><strong>{{ appName(record.app_id) }}</strong><span>{{ formatTime(record.at) }} · {{ record.platform }} / {{ record.user_id }}</span></div>
        <p class="hint">最多保留最近 100 条。这里的记录不代表应用当前仍有活跃会话。</p>
      </div>
    </section>
    <Modal v-if="editor" :title="creating ? '添加应用' : '管理应用'" initial-focus="#trusted-name" @close="closeEditor">
      <form id="trusted-app-form" class="stack" @submit.prevent="save">
        <div class="field"><label for="trusted-name">应用名称</label><input id="trusted-name" v-model="editor.name" class="input" required maxlength="64" placeholder="例如：入画管理台" /><span class="hint">显示在登录确认回执中。</span></div>
        <div class="field"><label for="trusted-id">应用标识</label><input id="trusted-id" v-model="editor.id" class="input mono" required pattern="[a-z][a-z0-9_-]{0,47}" :disabled="!creating" placeholder="例如：ruhua" /><span class="hint">使用小写英文字母、数字、下划线或连字符，以字母开头。创建后不可修改。</span></div>
        <div class="field"><label for="trusted-profile">确认机器人</label><AppSelect id="trusted-profile" v-model="editor.profile_id" :options="profileOptions" placeholder="请选择机器人" /><span class="hint">仅该机器人的主人可以确认登录。</span></div>
        <p v-if="profileReason(editor.profile_id)" class="hint">{{ profileReason(editor.profile_id) }} · <a href="/bot">前往开启管理员快速登录</a></p>
        <AppSwitch v-if="!creating" :model-value="!editor.disabled" :disabled="busy" @update:model-value="editor.disabled = !$event">启用此应用</AppSwitch>
        <p v-if="!creating" class="hint">停用或切换机器人后，待确认请求会立即失效。已登录会话需在对应应用中撤销。</p>
        <button v-if="!creating" type="button" class="btn danger" :disabled="busy" @click="rotate">重置连接密钥</button>
      </form>
      <template #footer><button class="btn" :disabled="busy" @click="closeEditor">取消</button><button class="btn primary" form="trusted-app-form" type="submit" :disabled="busy || !editor.profile_id || (!editor.disabled && !!profileReason(editor.profile_id))">{{ busy ? '保存中…' : creating ? '创建应用' : '保存' }}</button></template>
    </Modal>
    <Modal v-if="issued" title="连接密钥已生成" initial-focus="#trusted-secret" @close="closeSecret">
      <div class="stack"><p>将以下信息配置到应用服务端。密钥仅显示这一次，不要放入网页或小程序代码中。</p>
        <div class="field"><label for="trusted-address">Diana 服务地址</label><input id="trusted-address" v-model="serviceURL" class="input mono" /><span class="hint">默认是当前访问地址，请确认应用服务端可以访问。公网部署请使用 HTTPS。</span></div>
        <div class="field"><label>应用标识</label><code>{{ issued.id }}</code></div>
        <div class="field"><label for="trusted-secret">应用密钥</label><input id="trusted-secret" :value="issued.secret" class="input mono" readonly :type="reveal ? 'text' : 'password'" autocomplete="off" /><div class="cluster"><button class="btn small" @click="reveal = !reveal">{{ reveal ? '隐藏' : '显示' }}</button><button class="btn small" @click="copy(issued.secret)">复制密钥</button></div></div>
        <AppSwitch v-model="savedSecret">我已安全保存连接信息</AppSwitch>
      </div><template #footer><button class="btn" @click="copyConnection">复制接入配置</button><button class="btn primary" :disabled="!savedSecret" @click="closeSecret">完成</button></template>
    </Modal>
  </div>
</template>
<script setup lang="ts">
import {computed,onMounted,ref} from 'vue';
import {Plus} from '@lucide/vue';
import AppSwitch from './AppSwitch.vue';
import AppSelect from './AppSelect.vue';
import Modal from './Modal.vue';
import LoadingSkeleton from './LoadingSkeleton.vue';
import {getTrustedLoginSettings,setTrustedLoginEnabled,saveTrustedLoginApp,rotateTrustedLoginSecret,type TrustedLoginApp,type TrustedLoginSettings} from '../api';
import {askConfirm} from '../lib/confirm';
import {toastError,toastSuccess} from '../lib/toast';
import {formatTime} from '../lib/format';
const settings=ref<TrustedLoginSettings|null>(null),loading=ref(true),busy=ref(false),error=ref('');
const editor=ref<Pick<TrustedLoginApp,'id'|'name'|'profile_id'|'disabled'>|null>(null),creating=ref(false);
const issued=ref<{id:string;secret:string}|null>(null),savedSecret=ref(false),reveal=ref(false),serviceURL=ref(window.location.origin);
const profileOptions=computed(()=> (settings.value?.profiles || []).map(p=>({value:p.id,label:p.name || p.id,hint:p.reason || p.platform})));
const profileName=(id:string)=>settings.value?.profiles.find(p=>p.id===id)?.name || id;
const profileReason=(id:string)=>!id ? '' : settings.value?.profiles.find(p=>p.id===id)?.reason ?? '机器人已不存在';
const appName=(id:string)=>settings.value?.apps.find(a=>a.id===id)?.name || id;
async function load(){loading.value=true;error.value='';try{settings.value=await getTrustedLoginSettings();}catch(e){error.value=e instanceof Error?e.message:'加载失败';}finally{loading.value=false;}}
async function action(task:()=>Promise<void>){if(busy.value)return;busy.value=true;try{await task();}catch(e){toastError(e instanceof Error?e.message:'操作失败');}finally{busy.value=false;}}
async function toggle(enabled:boolean){if(!enabled && !(await askConfirm({title:'关闭第三方授权',message:'停止所有应用的新登录确认，并使待确认请求失效。已登录的会话需要在对应应用中撤销。',confirmLabel:'关闭',danger:true})))return;await action(async()=>{await setTrustedLoginEnabled(enabled);await load();});}
function openEditor(app?:TrustedLoginApp){creating.value=!app;editor.value=app?{id:app.id,name:app.name,profile_id:app.profile_id,disabled:app.disabled}:{id:'',name:'',profile_id:'',disabled:false};}
function closeEditor(){if(!busy.value)editor.value=null;}
function showSecret(value:{id:string;secret:string}){issued.value=value;savedSecret.value=false;reveal.value=false;editor.value=null;}
async function save(){const app=editor.value;if(!app)return;await action(async()=>{const result=await saveTrustedLoginApp(app,creating.value);if(result.secret)showSecret(result);else{editor.value=null;toastSuccess('应用设置已保存');}await load();});}
async function rotate(){const app=editor.value;if(!app)return;if(!(await askConfirm({title:'重置连接密钥',message:'旧密钥和待确认请求会立即失效。请将新密钥更新到应用服务端，已有应用会话不会被撤销。',confirmLabel:'重置密钥',danger:true})))return;await action(async()=>{showSecret(await rotateTrustedLoginSecret(app.id));await load();});}
async function copy(text:string){try{await navigator.clipboard.writeText(text);toastSuccess('已复制');}catch{reveal.value=true;toastError('复制失败，请选中并手动复制');}}
async function copyConnection(){if(!issued.value)return;await copy(JSON.stringify({diana_url:serviceURL.value,app_id:issued.value.id,app_secret:issued.value.secret,profile_id:settings.value?.apps.find(a=>a.id===issued.value?.id)?.profile_id},null,2));}
async function closeSecret(){if(!savedSecret.value && !(await askConfirm({title:'尚未保存连接信息',message:'关闭后无法再次查看此密钥。丢失后只能重置。',confirmLabel:'仍然关闭'})))return;issued.value=null;reveal.value=false;}
onMounted(load);
</script>
<style scoped>
.trusted-login-settings .card-header{gap:16px;flex-wrap:wrap}.trusted-login-settings .row-item{gap:16px}.trusted-login-settings .row-main{min-width:0;overflow-wrap:anywhere}.login-record{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;padding:10px 0;border-bottom:1px solid var(--border)}.login-record span{font-size:12px;color:var(--muted)}
</style>
