<template>
  <section class="extension-manager">
    <header class="view-header">
      <div class="view-title"><h1>{{ kind === 'skill' ? 'Skills' : 'MCP' }}</h1><p>配置全局共享 · {{ botScope ? '启用状态仅影响当前机器人' : '选择机器人后调整启用状态' }}</p></div>
      <div class="view-actions"><button class="btn" :disabled="loading" @click="load"><RefreshCw :size="15" />刷新</button><button class="btn primary" @click="openNew"><Plus :size="15" />{{ kind === 'skill' ? '添加 Skill' : '添加 MCP' }}</button></div>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取扩展…</p>
    <div v-else class="extension-list">
      <article v-for="item in items" :key="item.id" class="extension-row">
        <div class="extension-info"><strong>{{ item.name }}</strong><p>{{ item.description || (item.transport === 'stdio' ? '本地进程' : item.transport === 'streamable_http' ? 'HTTP MCP' : '') }}</p><small>{{ item.source || '本地扩展' }} · {{ item.managed ? '可管理' : '只读' }}</small><p v-if="item.error" class="error-text">{{ item.error }}</p></div>
        <span v-if="item.available===false" class="badge">全局停用</span>
        <label v-if="botScope" class="switch"><input type="checkbox" :checked="item.enabled" :disabled="busy === item.id || item.available===false" :aria-label="`启用 ${item.name}`" @change="toggle(item)" /><span class="track"></span></label>
        <button class="btn icon-only" :aria-label="`查看或编辑 ${item.name}`" title="查看或编辑" @click="edit(item)"><Settings2 :size="16" /></button>
        <button v-if="item.managed" class="btn ghost danger icon-only" :aria-label="`删除 ${item.name}`" title="删除" @click="remove(item)"><Trash2 :size="16" /></button>
      </article>
      <p v-if="!items.length">还没有{{ kind === 'skill' ? '自定义 Skill' : 'MCP 服务' }}。</p>
    </div>
    <Modal v-if="editing" :title="`${existing ? '编辑' : '添加'} ${kind === 'skill' ? 'Skill' : 'MCP'}`" wide @close="closeEditor">
      <div class="extension-form">
        <label class="field">名称<input v-model.trim="form.name" class="input" :disabled="existing || readonly" /></label>
        <template v-if="kind === 'skill'">
          <div v-if="!existing" class="segmented" role="group" aria-label="导入方式"><button type="button" :class="{active:!fromURL}" @click="fromURL=false">正文 / 文件</button><button type="button" :class="{active:fromURL}" @click="fromURL=true">网址</button></div>
          <label v-if="fromURL && !existing" class="field">SKILL.md 或 ZIP 地址<input v-model.trim="form.source_url" class="input" type="url" placeholder="https://…" /></label>
          <template v-else><label v-if="!readonly" class="field">导入 SKILL.md<input type="file" accept=".md,text/markdown" @change="importFile" /></label><label class="field">SKILL.md<textarea v-model="form.content" class="input code-input" :readonly="readonly" rows="14" spellcheck="false"></textarea></label></template>
        </template>
        <template v-else>
          <div class="segmented" role="group" aria-label="MCP 连接方式"><button type="button" :class="{active:transport==='http'}" @click="transport='http'">HTTP</button><button type="button" :class="{active:transport==='stdio'}" @click="transport='stdio'">stdio</button></div>
          <label v-if="transport==='http'" class="field">服务地址<input v-model.trim="form.url" class="input" type="url" placeholder="https://example.com/mcp" /></label>
          <template v-else><label class="field">启动命令<input v-model.trim="form.command" class="input" placeholder="npx" /></label><label class="field">参数（每行一个）<textarea v-model="form.args" class="input code-input" rows="3"></textarea></label><label class="field">工作目录<input v-model.trim="form.cwd" class="input" /></label></template>
          <label class="field">{{ transport==='http' ? '请求头 JSON' : '环境变量 JSON' }}<textarea v-model="secrets" class="input code-input" rows="4" spellcheck="false" placeholder='{"Authorization":"Bearer …"}'></textarea></label>
          <p class="hint">已有凭据留空表示保留；勾选下方项目才清除。</p>
          <label v-for="key in secretKeys" :key="key" class="secret-clear"><input v-model="clearSecrets" type="checkbox" :value="key" />清除 {{ key }}</label>
          <div class="extension-grid"><label class="field">连接超时（秒）<input v-model.number="form.startup_timeout_sec" class="input" type="number" min="1" max="120" /></label><label class="field">工具超时（秒）<input v-model.number="form.tool_timeout_sec" class="input" type="number" min="1" max="300" /></label></div>
          <label class="field">允许的工具（每行一个，留空全部）<textarea v-model="form.enabled_tools" class="input code-input" rows="2"></textarea></label>
          <label class="field">禁用的工具（每行一个）<textarea v-model="form.disabled_tools" class="input code-input" rows="2"></textarea></label>
          <label class="switch"><input v-model="form.enabled" type="checkbox" /><span class="track"></span>服务可用</label>
          <p class="hint">测试连接会访问服务；stdio 会启动配置的本地进程。仅主人会话可调用自定义扩展工具。</p>
          <p v-if="tested" role="status">连接成功，发现 {{ discovered.length }} 个工具</p><ul v-if="discovered.length"><li v-for="name in discovered" :key="name" class="tool-name">{{ name }}</li></ul>
        </template>
        <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      </div>
      <template #footer><button v-if="kind==='mcp'" class="btn" :disabled="saving" @click="testConnection"><PlugZap :size="15" />测试连接</button><button class="btn" :disabled="saving" @click="closeEditor">关闭</button><button v-if="!readonly" class="btn primary" :disabled="saving" @click="save"><Save :size="15" />保存</button></template>
    </Modal>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { Plus, RefreshCw, Settings2, Trash2, Save, PlugZap } from '@lucide/vue';
import Modal from './Modal.vue';
import { botScope } from '../bot-scope';
import { listManagedExtensions, manageExtension, type ManagedExtension } from '../api';
import { askConfirm } from '../confirm';
import { toastError, toastSuccess } from '../toast';

const props=defineProps<{kind:'skill'|'mcp'}>();
const items=ref<ManagedExtension[]>([]),loading=ref(false),loadError=ref(''),busy=ref(''),editing=ref(false),existing=ref(false),readonly=ref(false),saving=ref(false),error=ref('');
const fromURL=ref(false),transport=ref<'http'|'stdio'>('http'),tested=ref(false),discovered=ref<string[]>([]),headers=ref('{}'),env=ref('{}'),configuredHeaders=ref<string[]>([]),configuredEnv=ref<string[]>([]),clearSecrets=ref<string[]>([]);
const blank=()=>({name:'',content:'',source_url:'',url:'',command:'',args:'',cwd:'',enabled:true,startup_timeout_sec:20,tool_timeout_sec:60,enabled_tools:'',disabled_tools:''});
const form=ref(blank());
const secrets=computed({get:()=>transport.value==='http'?headers.value:env.value,set:v=>{if(transport.value==='http')headers.value=v;else env.value=v}});
const secretKeys=computed(()=>transport.value==='http'?configuredHeaders.value:configuredEnv.value);
const snapshot=ref('');
const state=()=>JSON.stringify([form.value,transport.value,headers.value,env.value,fromURL.value,clearSecrets.value]);
let generation=0;
async function load(){const current=++generation;loading.value=true;loadError.value='';try{const result=await listManagedExtensions(botScope.value);if(current===generation)items.value=result.items.filter(i=>i.kind===props.kind)}catch(e){if(current===generation)loadError.value=String(e instanceof Error?e.message:e)}finally{if(current===generation)loading.value=false}}
function openNew(){form.value=blank();headers.value=env.value='{}';configuredHeaders.value=[];configuredEnv.value=[];clearSecrets.value=[];fromURL.value=false;transport.value='http';existing.value=readonly.value=false;error.value='';tested.value=false;discovered.value=[];editing.value=true;snapshot.value=state()}
async function edit(item:ManagedExtension){try{const data=await manageExtension<any>({operation:'read',kind:props.kind,name:item.name});openNew();existing.value=true;readonly.value=!item.managed;form.value.name=item.name;if(props.kind==='skill')form.value.content=data.content;else{const c=data.config;Object.assign(form.value,c,{args:(c.args||[]).join('\n'),enabled_tools:(c.enabled_tools||[]).join('\n'),disabled_tools:(c.disabled_tools||[]).join('\n'),enabled:c.enabled!==false});transport.value=c.command?'stdio':'http';headers.value=JSON.stringify(c.headers||{},null,2);env.value=JSON.stringify(c.env||{},null,2);configuredHeaders.value=data.configured_headers||[];configuredEnv.value=data.configured_env||[]}snapshot.value=state()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
async function closeEditor(){if(!editing.value||saving.value)return;if(!readonly.value&&snapshot.value!==state()&&!await askConfirm({title:'放弃未保存的修改？',message:'本次编辑尚未保存。',confirmLabel:'放弃'}))return;editing.value=false}
async function importFile(event:Event){const file=(event.target as HTMLInputElement).files?.[0];if(!file)return;if(file.size>2*1024*1024){error.value='文件不能超过 2 MB';return}form.value.content=await file.text();fromURL.value=false}
const lines=(s:string)=>s.split('\n').map(x=>x.trim()).filter(Boolean);
function stringMap(raw:string){const result=JSON.parse(raw||'{}');if(!result||Array.isArray(result)||typeof result!=='object'||Object.values(result).some(v=>typeof v!=='string'))throw new Error('凭据必须是字符串键值 JSON 对象');return result}
function payload(operation:string){return {operation,kind:props.kind,name:form.value.name,replace:existing.value,content:fromURL.value?'':form.value.content,source_url:fromURL.value?form.value.source_url:'',config:{enabled:form.value.enabled,url:transport.value==='http'?form.value.url:'',command:transport.value==='stdio'?form.value.command:'',args:transport.value==='stdio'?lines(form.value.args):[],cwd:transport.value==='stdio'?form.value.cwd:'',headers:transport.value==='http'?stringMap(headers.value):{},env:transport.value==='stdio'?stringMap(env.value):{},startup_timeout_sec:form.value.startup_timeout_sec,tool_timeout_sec:form.value.tool_timeout_sec,enabled_tools:lines(form.value.enabled_tools),disabled_tools:lines(form.value.disabled_tools)},clear_headers:transport.value==='http'?clearSecrets.value:[],clear_env:transport.value==='stdio'?clearSecrets.value:[]}}
async function save(){saving.value=true;error.value='';try{await manageExtension(payload('save'));editing.value=false;toastSuccess('扩展已保存，后续会话生效');await load()}catch(e){error.value=String(e instanceof Error?e.message:e)}finally{saving.value=false}}
async function testConnection(){saving.value=true;error.value='';tested.value=false;discovered.value=[];try{const result=await manageExtension<{connected:boolean;tools:string[]}>(payload('test'));tested.value=result.connected;discovered.value=result.tools}catch(e){error.value=String(e instanceof Error?e.message:e)}finally{saving.value=false}}
async function toggle(item:ManagedExtension){const profile=botScope.value;if(!profile)return;busy.value=item.id;try{await manageExtension({operation:'enabled',kind:props.kind,name:item.name,profile_id:profile,enabled:!item.enabled});await load()}catch(e){toastError(String(e instanceof Error?e.message:e))}finally{busy.value=''}}
async function remove(item:ManagedExtension){if(!await askConfirm({title:`删除 ${item.name}？`,message:'全局删除会影响使用它的所有机器人。',confirmLabel:'删除',danger:true}))return;try{await manageExtension({operation:'delete',kind:props.kind,name:item.name});await load()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
watch(() => state(),()=>{tested.value=false;discovered.value=[]});
async function prepareLeave(){await closeEditor();return !editing.value}
defineExpose({prepareLeave});
watch(botScope,load);onMounted(load);
</script>

<style scoped>
.extension-manager{padding-top:20px}.extension-list{border-top:1px solid var(--border)}.extension-row{display:flex;align-items:center;gap:12px;padding:18px 0;border-bottom:1px solid var(--border)}.extension-info{flex:1;min-width:0;overflow-wrap:anywhere}.extension-info p{margin:6px 0;color:var(--muted)}.extension-info small{color:var(--muted)}.extension-form{display:grid;gap:14px}.code-input{font-family:monospace;resize:vertical;min-width:0;white-space:pre-wrap}.extension-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.secret-clear{display:flex;gap:8px;align-items:center}.tool-name{overflow-wrap:anywhere}.error-text{color:var(--danger)}@media(max-width:600px){.extension-row{gap:6px;flex-wrap:wrap}.extension-info{flex-basis:100%}.extension-grid{grid-template-columns:1fr}}
</style>
