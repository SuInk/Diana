<template>
  <section class="extension-manager">
    <header class="view-header">
      <div class="view-title"><h2>{{ kind === 'skill' ? 'Skills' : 'MCP' }}</h2><p>配置全局共享 · {{ botScope ? '启用状态与权限仅影响当前机器人' : '选择机器人后调整启用状态' }}</p></div>
      <div class="view-actions"><button class="btn" :disabled="loading" @click="load"><RefreshCw :size="15" />刷新</button><button v-if="kind==='mcp'" class="btn" @click="openPresets"><Blocks :size="15" />预设</button><button class="btn primary" @click="openNew"><Plus :size="15" />{{ kind === 'skill' ? '添加 Skill' : '添加 MCP' }}</button></div>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取扩展…</p>
    <div v-else class="extension-list">
      <article v-for="item in items" :key="item.id" class="extension-row">
        <div class="extension-info">
          <strong>{{ item.name }}<span v-if="item.available===false" class="badge">全局停用</span></strong>
          <p>{{ item.description || (item.transport === 'stdio' ? '本地进程' : item.transport === 'streamable_http' ? 'HTTP MCP' : '') }}</p>
          <small>{{ item.source || '本地扩展' }} · {{ item.managed ? '可管理' : '只读' }}<template v-if="item.bundled"> · <span title="目录里带脚本或资源；群成员没有命令和文件工具，这部分在成员会话里不会执行">含脚本</span></template><template v-if="audienceSummary(item)"> · <span class="extension-audience-note">{{ audienceSummary(item) }}</span></template><template v-if="item.keywords?.length"> · <span title="SKILL.md 自己声明的触发词：最近两条消息里命中就把正文带进上下文">触发词 {{ item.keywords.join('、') }}</span></template></small>
          <p v-if="item.error" class="error-text">{{ item.error }}</p>
        </div>
        <!-- 停用 / 仅主人 / 群成员是同一件事的三档，合成一个控件：两个开关并排时
             没人分得清哪个管什么，状态也要扫一眼就看得见。 -->
        <div v-if="botScope" class="segmented extension-state" role="group" :aria-label="`${item.name} 在当前机器人的状态`">
          <button v-for="state in extensionStates" :key="state.value" type="button" :class="{active: currentState(item) === state.value}" :disabled="busy === item.id || item.available===false" :title="state.hint" @click="setState(item, state.value)">{{ state.label }}</button>
        </div>
        <!-- Skill 的常驻档位：常驻就是正文直接进上下文，不必再 read_skill。MCP 的档位
             按服务算，放在「上下文」标签里，和内置工具排在一起。 -->
        <div v-if="botScope && kind === 'skill'" class="segmented extension-state" role="group" :aria-label="`${item.name} 的上下文档位`">
          <button v-for="tier in residencyTiers" :key="String(tier.value)" type="button" :class="{active: residency(item) === tier.value}" :disabled="busy === item.id || item.available===false" :title="tierHint(tier.value, item)" @click="setResidency(item, tier.value)">{{ tier.label }}</button>
        </div>
        <!-- 和编辑、删除同一款图标按钮：名单是「去改」的入口，改完的结果写在上面那行小字里。
             档位没放开时留着但置灰，免得每行的按钮左右错位。 -->
        <button v-if="botScope" class="btn icon-only" :disabled="busy === item.id || !isOpenTier(item)" :aria-label="`设置 ${item.name} 的开放对象`" :title="audienceTitle(item)" @click="openAccess(item)"><Users :size="16" /></button>
        <button class="btn icon-only" :aria-label="`查看或编辑 ${item.name}`" title="查看或编辑" @click="edit(item)"><Settings2 :size="16" /></button>
        <button v-if="item.managed" class="btn ghost danger icon-only" :aria-label="`删除 ${item.name}`" title="删除" @click="remove(item)"><Trash2 :size="16" /></button>
        <!-- 只读扩展删不掉，但位置要留着：否则每行的开关和按钮左右错开一截。 -->
        <span v-else class="extension-action-slot" aria-hidden="true"></span>
      </article>
      <p v-if="!items.length">还没有{{ kind === 'skill' ? '自定义 Skill' : 'MCP 服务' }}。</p>
    </div>
    <Modal v-if="presetsOpen" :title="preset ? `添加 ${preset.title}` : '从预设添加 MCP'" @close="closePresets">
      <div class="extension-form">
        <template v-if="!preset">
          <p class="hint">预设只是帮你填好参数，装上之后就是一条普通的 MCP，改配置、停用、删除都和手工添加的一样。服务本身要自己跑，Diana 不打包别人的二进制。</p>
          <p v-if="presetError" class="error-text" role="alert">{{ presetError }}</p>
          <article v-for="entry in presets" :key="entry.preset.id" class="preset-row">
            <div class="extension-info">
              <strong>{{ entry.preset.title }}<span v-if="entry.installed" class="badge">已添加</span></strong>
              <p>{{ entry.preset.summary }}</p>
              <small v-if="entry.preset.docs_url"><a :href="entry.preset.docs_url" target="_blank" rel="noreferrer noopener">官方文档</a></small>
            </div>
            <button class="btn" @click="pickPreset(entry.preset)">{{ entry.installed ? '再装一个' : '添加' }}</button>
          </article>
          <p v-if="!presets.length && !presetError">还没有内置预设。</p>
        </template>
        <template v-else>
          <p class="hint">{{ preset.summary }}<template v-if="preset.docs_url"> <a :href="preset.docs_url" target="_blank" rel="noreferrer noopener">官方文档</a></template></p>
          <div v-if="preset.transports.length > 1" class="segmented" role="group" aria-label="接入方式"><button v-for="option in preset.transports" :key="option.id" type="button" :class="{active: presetTransport === option.id}" @click="presetTransport = option.id">{{ option.label }}</button></div>
          <p v-if="presetTransportHint" class="hint">{{ presetTransportHint }}</p>
          <label class="field">名称<input v-model.trim="presetName" class="input" /><span class="hint">装上后这条 MCP 的名字，装第二个同类服务时改一下。</span></label>
          <label v-for="field in presetFields" :key="field.key" class="field">
            <span>{{ field.label }}<template v-if="field.required"> *</template></span>
            <input v-model.trim="presetValues[field.key]" class="input" :type="field.secret ? 'password' : 'text'" :placeholder="field.placeholder" :autocomplete="field.secret ? 'new-password' : 'off'" />
            <span v-if="field.hint" class="hint">{{ field.hint }}</span>
          </label>
          <p v-if="presetError" class="error-text" role="alert">{{ presetError }}</p>
        </template>
      </div>
      <template #footer><button v-if="preset" class="btn" :disabled="presetSaving" @click="preset=null">返回</button><button class="btn" :disabled="presetSaving" @click="closePresets">关闭</button><button v-if="preset" class="btn primary" :disabled="presetSaving" @click="savePreset"><Save :size="15" />添加</button></template>
    </Modal>
    <Modal v-if="accessFor" :title="`${accessFor.name} 的开放对象`" @close="accessFor=null">
      <div class="extension-form">
        <p class="hint">两个名单都留空 = 这台机器人的所有群成员。都填则要同时满足：名单里的人，且只在这些群里。主人不受名单限制。</p>
        <div class="field"><label for="extension-access-users">用户</label><IdChipInput input-id="extension-access-users" :model-value="accessUsers" placeholder="填账号后回车，留空 = 所有群成员" :resolve-names="resolveAccountNames" @update:model-value="accessUsers = $event" /></div>
        <div class="field"><label for="extension-access-groups">群号</label><IdChipInput input-id="extension-access-groups" :model-value="accessGroups" placeholder="填群号后回车，留空 = 不限群" @update:model-value="accessGroups = $event" /></div>
        <p v-if="accessError" class="error-text" role="alert">{{ accessError }}</p>
      </div>
      <template #footer><button class="btn" :disabled="savingAccess" @click="accessFor=null">关闭</button><button class="btn primary" :disabled="savingAccess" @click="saveAudience"><Save :size="15" />保存</button></template>
    </Modal>
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
          <p class="hint">测试连接会访问服务；stdio 会启动配置的本地进程。谁能调用这些工具在列表里按机器人设置，默认仅主人。</p>
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
import { Blocks, Plus, RefreshCw, Settings2, Trash2, Save, PlugZap, Users } from '@lucide/vue';
import Modal from './Modal.vue';
import { botScope } from '../bot-scope';
import { fetchAssistantUserNames, listMCPPresets, listManagedExtensions, manageExtension, type MCPPreset, type ManagedExtension } from '../api';
import IdChipInput from './IdChipInput.vue';
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
// 预设：服务端给字段清单，这里只负责渲染和回填，拼配置仍在服务端做。
const presetsOpen=ref(false),presets=ref<{preset:MCPPreset;installed:boolean}[]>([]),preset=ref<MCPPreset|null>(null),presetTransport=ref(''),presetValues=ref<Record<string,string>>({}),presetName=ref(''),presetSaving=ref(false),presetError=ref('');
const presetFields=computed(()=>preset.value?.transports.find(t=>t.id===presetTransport.value)?.fields||[]);
const presetTransportHint=computed(()=>preset.value?.transports.find(t=>t.id===presetTransport.value)?.hint||'');
async function openPresets(){presetsOpen.value=true;preset.value=null;presetError.value='';try{presets.value=(await listMCPPresets()).items}catch(e){presetError.value=String(e instanceof Error?e.message:e)}}
function closePresets(){if(presetSaving.value)return;presetsOpen.value=false;preset.value=null}
function pickPreset(value:MCPPreset){preset.value=value;presetTransport.value=value.transports[0]?.id||'';presetValues.value={};presetName.value=items.value.some(i=>i.name===value.name)?`${value.name}-2`:value.name;presetError.value=''}
async function savePreset(){if(!preset.value)return;presetSaving.value=true;presetError.value='';try{await manageExtension({operation:'preset_save',kind:'mcp',name:presetName.value,preset:preset.value.id,transport:presetTransport.value,values:presetValues.value});presetsOpen.value=false;preset.value=null;toastSuccess('已添加，默认仅主人可用，可在列表里开放');await load()}catch(e){presetError.value=String(e instanceof Error?e.message:e)}finally{presetSaving.value=false}}
const accessFor=ref<ManagedExtension|null>(null),accessUsers=ref<string[]>([]),accessGroups=ref<string[]>([]),accessError=ref(''),savingAccess=ref(false);
async function resolveAccountNames(ids:string[]):Promise<Record<string,string>>{const response=await fetchAssistantUserNames(ids);return response.names??{}}
const extensionStates=[{value:'off',label:'停用',hint:'这台机器人不用它'},{value:'owner',label:'仅主人',hint:'只有主人会话能用'},{value:'admins',label:'群管',hint:'群主和群管理员也能用；平台给不出身份时按普通成员处理'},{value:'members',label:'群成员',hint:'群成员也能用，可再限定对象'}] as const;
type ExtensionState=typeof extensionStates[number]['value'];
const currentState=(item:ManagedExtension):ExtensionState=>!item.enabled?'off':!item.members_enabled?'owner':item.member_audience?.min_role==='admin'?'admins':'members';
const isOpenTier=(item:ManagedExtension)=>{const state=currentState(item);return state==='members'||state==='admins'};
// 小字只写「被收窄成什么样」，没收窄就不写：档位那一段已经说过谁能用了。
function audienceSummary(item:ManagedExtension){if(!isOpenTier(item))return '';const users=item.member_audience?.users?.length||0,groups=item.member_audience?.groups?.length||0;if(!users&&!groups)return '';return `限定 ${[users?`${users} 人`:'',groups?`${groups} 群`:''].filter(Boolean).join(' · ')}`}
function audienceTitle(item:ManagedExtension){if(!isOpenTier(item))return '开放给群管或群成员后，才能再限定具体是谁';return audienceSummary(item)?`开放对象：${audienceSummary(item)}`:'设置开放对象，现在是这一档的所有人'}
function memberRisk(item:ManagedExtension,state:ExtensionState){const who=state==='admins'?'群主和群管理员':'群里任何人';
 if(props.kind==='skill')return `正文对${state==='admins'?'群主和群管理员':'群里任何人'}可读。${item.bundled?'目录里的脚本在成员会话不会执行（成员没有命令和文件工具），':''}真正会发生的是模型按它去用搜索、网页渲染、订阅这些已有的工具。`;
 return `${who}都能在对话里触发这个服务的工具。只对只读查询类服务开放。`}
async function setState(item:ManagedExtension,state:ExtensionState){const profile=botScope.value;const now=currentState(item);if(!profile||now===state)return;
 const opening=(state==='members'||state==='admins')&&(now==='off'||now==='owner'||(now==='admins'&&state==='members'));
 if(opening&&!await askConfirm({title:`让${state==='admins'?'群主和管理员':'群成员'}使用 ${item.name}？`,message:memberRisk(item,state),confirmLabel:'开放'}))return;
 const members=state==='members'||state==='admins';
 busy.value=item.id;
 try{
  const base={kind:props.kind,name:item.name,profile_id:profile};
  const audience={min_role:state==='admins'?'admin':'',users:item.member_audience?.users||[],groups:item.member_audience?.groups||[]};
  // 收紧的那一步先做：先摘权限再改启用，不会出现「已开放但还没限制住」的瞬间。
  if(!members&&item.members_enabled)await manageExtension({...base,operation:'members',enabled:false});
  if(members&&audience.min_role!==(item.member_audience?.min_role||''))await manageExtension({...base,operation:'audience',audience});
  if((state==='off')!==!item.enabled)await manageExtension({...base,operation:'enabled',enabled:state!=='off'});
  if(members&&!item.members_enabled)await manageExtension({...base,operation:'members',enabled:true});
  await load();
 }catch(e){toastError(String(e instanceof Error?e.message:e));await load()}finally{busy.value=''}}
// 默认档就是「只进目录」。正文常驻解决的是另一个问题：上下文一长，模型按目录去
// read_skill 这一步经常不做，写得再细的 skill 也读不到。
const residencyTiers=[{value:null,label:'默认'},{value:true,label:'常驻'},{value:false,label:'按需'}] as const;
// 「默认」对每个 skill 的含义不一样，取决于它自己声明没声明触发词，所以提示按行算。
function tierHint(value:boolean|null,item:ManagedExtension){
 if(value===true)return '正文每轮都进上下文，模型不必再 read_skill；长 skill 每轮都要算钱';
 if(value===false)return '只进目录，用到再 read_skill；声明过触发词也不再自动带正文';
 return item.keywords?.length?`跟随默认：最近两条消息命中「${item.keywords.join('、')}」时才带正文，其余时候只进目录`:'跟随默认：只进目录，用到再 read_skill。在 SKILL.md 的 keywords 里写上触发词，就能改成命中才带正文';
}
const residency=(item:ManagedExtension)=>item.resident===undefined?null:item.resident;
async function setResidency(item:ManagedExtension,value:boolean|null){const profile=botScope.value;if(!profile||residency(item)===value)return;busy.value=item.id;
 try{const payload:Record<string,unknown>={operation:'residency',kind:props.kind,name:item.name,profile_id:profile};if(value!==null)payload.resident=value;await manageExtension(payload);toastSuccess('档位已更新，后续会话生效');await load()}catch(e){toastError(String(e instanceof Error?e.message:e))}finally{busy.value=''}}
function openAccess(item:ManagedExtension){accessFor.value=item;accessUsers.value=[...(item.member_audience?.users||[])];accessGroups.value=[...(item.member_audience?.groups||[])];accessError.value=''}
async function saveAudience(){const item=accessFor.value,profile=botScope.value;if(!item||!profile)return;savingAccess.value=true;accessError.value='';try{await manageExtension({operation:'audience',kind:props.kind,name:item.name,profile_id:profile,audience:{min_role:item.member_audience?.min_role||'',users:accessUsers.value,groups:accessGroups.value}});accessFor.value=null;toastSuccess('开放对象已更新，后续会话生效');await load()}catch(e){accessError.value=String(e instanceof Error?e.message:e)}finally{savingAccess.value=false}}
async function remove(item:ManagedExtension){if(!await askConfirm({title:`删除 ${item.name}？`,message:'全局删除会影响使用它的所有机器人。',confirmLabel:'删除',danger:true}))return;try{await manageExtension({operation:'delete',kind:props.kind,name:item.name});await load()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
watch(() => state(),()=>{tested.value=false;discovered.value=[]});
async function prepareLeave(){await closeEditor();return !editing.value}
defineExpose({prepareLeave});
watch(botScope,load);onMounted(load);
</script>

<style scoped>
.extension-manager{padding-top:20px}.preset-row{display:flex;align-items:center;gap:12px;padding:12px 0;border-top:1px solid var(--border)}.extension-list{border-top:1px solid var(--border)}.extension-row{display:flex;align-items:center;gap:12px;padding:18px 0;border-bottom:1px solid var(--border)}.extension-info{flex:1;min-width:0;overflow-wrap:anywhere}.extension-info p{margin:6px 0;color:var(--muted)}.extension-info small{color:var(--muted)}.extension-form{display:grid;gap:14px}.code-input{font-family:monospace;resize:vertical;min-width:0;white-space:pre-wrap}.extension-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.secret-clear{display:flex;gap:8px;align-items:center}.extension-info strong{display:flex;align-items:center;gap:8px}.extension-state{flex-shrink:0}.extension-state button:disabled{opacity:.45;cursor:not-allowed}.extension-audience-note{color:var(--text-secondary)}.extension-action-slot{width:34px;flex-shrink:0}.tool-name{overflow-wrap:anywhere}.error-text{color:var(--danger)}@media(max-width:600px){.extension-row{gap:6px;flex-wrap:wrap}.extension-info{flex-basis:100%}.extension-grid{grid-template-columns:1fr}}
</style>
