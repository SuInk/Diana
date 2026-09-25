<template>
  <section class="extension-manager">
    <!-- 工具条和插件页共用 .plugins-view-header 那几条：控件同高、不换行。 -->
    <header class="view-header plugins-view-header">
      <div class="view-title"><h2>{{ kind === 'skill' ? 'Skills' : 'MCP' }}</h2><p>配置全局共享 · {{ botScope ? '启用状态与权限仅影响当前机器人' : kind === 'mcp' ? '这里的开关是所有机器人的默认，选择机器人后可单独调整' : '选择机器人后调整启用状态' }}</p></div>
      <div class="view-actions">
        <div class="plugin-search">
          <Search :size="14" aria-hidden="true" />
          <input v-model="query" class="input" type="search" :placeholder="`搜索${kind === 'skill' ? ' Skill' : ' MCP'}名称或说明`" :aria-label="`搜索${kind === 'skill' ? ' Skill' : ' MCP'}`" />
        </div>
        <div class="segmented plugin-status-filter" role="radiogroup" aria-label="按状态筛选">
          <button v-for="option in statusFilters" :key="option.value" type="button" role="radio" :aria-checked="status === option.value" :class="{active: status === option.value}" @click="status = option.value">
            <span>{{ option.label }}</span>
            <span class="plugin-filter-count">{{ option.count }}</span>
          </button>
        </div>
        <div class="segmented plugin-layout-switch" role="group" aria-label="排列方式">
          <button type="button" :class="{active: layout === 'tiles'}" title="方块：一行一行往右排" aria-label="方块排列" @click="setLayout('tiles')"><LayoutGrid :size="14" aria-hidden="true" /></button>
          <button type="button" :class="{active: layout === 'rows'}" title="横排：一行一个，信息更紧凑" aria-label="横排排列" @click="setLayout('rows')"><Rows3 :size="14" aria-hidden="true" /></button>
        </div>
        <button class="btn" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{spin: loading}" />刷新</button>
        <button class="btn primary" @click="openNew"><Plus :size="15" />{{ kind === 'skill' ? '添加 Skill' : '添加 MCP' }}</button>
      </div>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取扩展…</p>
    <!-- 版式跟插件页走：同一套卡片，扫一眼就知道这三处（插件 / Skills / MCP）是一类东西。 -->
    <div v-else class="extension-list" :class="layout === 'rows' ? 'plugin-rows' : 'plugin-tiles'">
      <article v-for="item in visibleItems" :key="item.id" class="plugin-card" :class="{off: (botScope || kind==='mcp') && !item.enabled}">
        <div class="plugin-card-head">
          <h2 class="plugin-card-name" :title="item.name">{{ item.name }}</h2>
          <!-- 卡片上始终只有一个开关，管什么跟着顶部选的范围走：选了机器人是「这台用不用」，
               全部机器人时（只有 MCP）是全局默认。给谁用、限定哪些人都在设置里。 -->
          <label v-if="botScope || kind==='mcp'" class="switch" :title="item.available===false ? '全局停用，先在设置里打开「服务可用」' : !botScope ? (item.enabled ? '所有机器人默认启用，点击统一停用' : '所有机器人默认停用，点击统一启用') : item.enabled ? '点击停用' : '点击启用'">
            <input type="checkbox" :checked="item.enabled" :disabled="busy === item.id || item.available===false" @change="toggleEnabled(item)" />
            <span class="track" aria-hidden="true"></span>
          </label>
        </div>
        <div class="cluster plugin-card-badges">
          <span v-if="item.available===false" class="badge warn">全局停用</span>
          <span v-if="botScope && item.enabled" class="badge">{{ tierLabel(item) }}</span>
          <span v-if="botScope && item.enabled && audienceSummary(item)" class="badge">{{ audienceSummary(item) }}</span>
          <span v-if="!item.managed" class="badge">只读</span>
          <span v-if="item.bundled" class="badge" title="目录里带脚本或资源；群成员没有命令和文件工具，这部分在成员会话里不会执行">含脚本</span>
          <span v-if="kind==='mcp'" class="badge mono">{{ item.transport === 'stdio' ? '本地进程' : 'HTTP' }}</span>
          <span v-if="item.keywords?.length" class="badge" title="SKILL.md 自己声明的触发词：最近两条消息里命中就把正文带进上下文">触发词 {{ item.keywords.join('、') }}</span>
        </div>
        <p class="plugin-card-desc" :title="item.description || item.source">{{ item.description || item.source || '本地扩展' }}</p>
        <p v-if="item.error" class="error-text">{{ item.error }}</p>
        <!-- Skill 正文带不带在「机器人 → 上下文」里改，和工具名单同一页：这里是「装了
             什么」，那里是「每轮花多少」。卡片上只显示当前档位。 -->
        <p v-if="botScope && kind === 'skill' && item.enabled && item.resident !== undefined" class="plugin-card-desc">正文：{{ item.resident ? '常驻' : '按需' }}（在机器人配置「上下文」里改）</p>
        <!-- 结构照插件卡片来：左边轻量信息、右边操作，横排版式靠这层排序。 -->
        <div class="plugin-card-bottom">
          <div class="plugin-card-meta"><span class="extension-audience-note">{{ item.managed ? '可管理' : '只读' }}</span></div>
          <footer class="plugin-card-foot">
            <button class="btn small" type="button" :aria-label="`查看或编辑 ${item.name}`" @click="edit(item)"><Settings2 :size="14" />设置</button>
            <button v-if="item.managed" class="btn small danger" type="button" :aria-label="`删除 ${item.name}`" title="删除" @click="remove(item)"><Trash2 :size="14" /></button>
          </footer>
        </div>
      </article>
      <!-- 内置预设默认就在列表里占一张卡：它是「有这么个服务，只是还没添加」，不是
           藏在某个按钮后面的目录。和插件页里没装的插件一样虚着边、没有开关。 -->
      <article v-for="entry in visiblePresets" :key="entry.preset.id" class="plugin-card uninstalled">
        <div class="plugin-card-head">
          <h2 class="plugin-card-name" :title="entry.preset.title">{{ entry.preset.title }}</h2>
        </div>
        <div class="cluster plugin-card-badges">
          <span class="badge">未添加</span>
          <span class="badge">内置预设</span>
        </div>
        <p class="plugin-card-desc" :title="entry.preset.summary">{{ entry.preset.summary }}</p>
        <div class="plugin-card-bottom">
          <div class="plugin-card-meta"><a v-if="entry.preset.docs_url" class="extension-doc-link" :href="entry.preset.docs_url" target="_blank" rel="noreferrer noopener">官方文档<ExternalLink :size="12" aria-hidden="true" /></a></div>
          <footer class="plugin-card-foot">
            <button class="btn small" type="button" :aria-label="`添加 ${entry.preset.title}`" @click="startPreset(entry.preset)"><Plus :size="14" />添加</button>
            <button class="btn small danger" type="button" :aria-label="`从列表里去掉 ${entry.preset.title}`" title="用不上，从列表里去掉" @click="hidePreset(entry.preset)"><Trash2 :size="14" /></button>
          </footer>
        </div>
      </article>
    </div>
    <p v-if="!loading && !visibleItems.length && !visiblePresets.length">{{ items.length || pendingPresets.length ? '没有匹配的扩展。' : `还没有${kind === 'skill' ? '自定义 Skill' : 'MCP 服务'}。` }}</p>
    <p v-if="!loading && hiddenPresets.length" class="hint"><button type="button" class="link-button" @click="showHiddenPresets">显示隐藏的预设（{{ hiddenPresets.length }}）</button></p>
    <Modal v-if="presetsOpen && preset" :title="`添加 ${preset.title}`" @close="closePresets">
      <div class="extension-form">
        <template v-if="preset">
          <p class="hint">{{ preset.summary }}<template v-if="preset.docs_url"> <a :href="preset.docs_url" target="_blank" rel="noreferrer noopener">官方文档</a></template></p>
          <div v-if="preset.transports.length > 1" class="segmented" role="group" aria-label="接入方式"><button v-for="option in preset.transports" :key="option.id" type="button" :class="{active: presetTransport === option.id}" @click="presetTransport = option.id">{{ option.label }}</button></div>
          <p v-if="presetTransportHint" class="hint">{{ presetTransportHint }}</p>
          <label class="field">名称<input v-model.trim="presetName" class="input" /><span class="hint">装上后这条 MCP 的名字，装第二个同类服务时改一下。</span></label>
          <label v-for="field in presetFields" :key="field.key" class="field">
            <span>{{ field.label }}<template v-if="field.required"> *</template></span>
            <input v-model.trim="presetValues[field.key]" class="input" :type="field.secret ? 'password' : 'text'" :placeholder="field.placeholder" :autocomplete="field.secret ? 'new-password' : 'off'" />
            <span v-if="field.hint" class="hint">{{ field.hint }}</span>
          </label>
          <p v-if="verifyNote" role="status">{{ verifyNote }}</p>
          <p v-if="presetError" class="error-text" role="alert">{{ presetError }}</p>
        </template>
      </div>
      <template #footer><button v-if="presetVerifiable" class="btn" :disabled="presetSaving||verifying" @click="verifyPreset"><KeyRound :size="15" />检测令牌</button><button class="btn" :disabled="presetSaving" @click="closePresets">关闭</button><button v-if="preset" class="btn primary" :disabled="presetSaving" @click="savePreset"><Save :size="15" />添加</button></template>
    </Modal>
    <Modal v-if="editing" :title="`${existing ? '编辑' : '添加'} ${kind === 'skill' ? 'Skill' : 'MCP'}`" wide @close="closeEditor">
      <div class="extension-form">
        <label class="field">名称<input v-model.trim="form.name" class="input" :disabled="existing || readonly" /></label>
        <template v-if="kind === 'skill'">
          <div v-if="!existing" class="segmented" role="group" aria-label="导入方式"><button type="button" :class="{active:!fromURL}" @click="fromURL=false">正文 / 文件</button><button type="button" :class="{active:fromURL}" @click="fromURL=true">网址</button></div>
          <label v-if="fromURL && !existing" class="field">SKILL.md 或 ZIP 地址<input v-model.trim="form.source_url" class="input" type="url" placeholder="https://…" /></label>
          <template v-else><label v-if="!readonly" class="field">导入 SKILL.md<input type="file" accept=".md,text/markdown" @change="importFile" /></label><label class="field">SKILL.md<textarea v-model="form.content" class="input code-input" :readonly="readonly" rows="14" spellcheck="false"></textarea></label></template>
        </template>
        <!-- 从预设装的服务，改配置还用那张表：地址和令牌在这里改，不用对着
             环境变量 JSON 猜键名。要改超时、工具名单这些，再切到高级配置。 -->
        <template v-else-if="editPreset && !editAdvanced">
          <p class="hint">{{ editPresetTransportLabel }}<template v-if="editPreset.docs_url"> · <a :href="editPreset.docs_url" target="_blank" rel="noreferrer noopener">官方文档</a></template></p>
          <label v-for="field in editPresetFields" :key="field.key" class="field">
            <span>{{ field.label }}<template v-if="field.required"> *</template></span>
            <input v-model.trim="editPresetValues[field.key]" class="input" :type="field.secret && !revealed ? 'password' : 'text'" :placeholder="field.secret ? (editPresetMasks[field.key] ? `已配置 ${editPresetMasks[field.key]}，留空保持原值` : '留空保持原值') : field.placeholder" :autocomplete="field.secret ? 'new-password' : 'off'" :disabled="readonly" />
            <span v-if="field.hint" class="hint">{{ field.hint }}</span>
          </label>
          <p v-if="(Object.keys(editPresetMasks).length || Object.values(editPresetValues).some(v => v.includes('****'))) && !readonly && !revealed" class="hint">令牌只显示掩码，<button type="button" class="link-button" :disabled="revealing" @click="revealSecrets">显示明文</button>。</p>
          <p v-if="verifyNote" role="status">{{ verifyNote }}</p>
          <p class="hint">超时、工具名单这些改不到的，切<button type="button" class="link-button" @click="editAdvanced=true">高级配置</button>。</p>
        </template>
        <template v-else>
          <p v-if="editPreset" class="hint">这条是从「{{ editPreset.title }}」预设装的，<button type="button" class="link-button" @click="editAdvanced=false">回到预设表单</button>改地址和令牌更省事。</p>
          <div class="segmented" role="group" aria-label="MCP 连接方式"><button type="button" :class="{active:transport==='http'}" @click="transport='http'">HTTP</button><button type="button" :class="{active:transport==='stdio'}" @click="transport='stdio'">stdio</button></div>
          <label v-if="transport==='http'" class="field">服务地址<input v-model.trim="form.url" class="input" type="url" placeholder="https://example.com/mcp" /></label>
          <template v-else><label class="field">启动命令<input v-model.trim="form.command" class="input" placeholder="npx" /></label><label class="field">参数（每行一个）<textarea v-model="form.args" class="input code-input" rows="3"></textarea></label><label class="field">工作目录<input v-model.trim="form.cwd" class="input" /></label></template>
          <label class="field">{{ transport==='http' ? '请求头 JSON' : '环境变量 JSON' }}<textarea v-model="secrets" class="input code-input" rows="4" spellcheck="false" placeholder='{"Authorization":"Bearer …"}'></textarea></label>
          <p class="hint">已保存的值只显示掩码：<strong>掩码原样留着或值留空 = 保持原值</strong>，<strong>删掉整行 = 删掉这一项</strong>。新加一行就是新增。<template v-if="storedSecrets && !readonly && !revealed">要看原文点<button type="button" class="link-button" :disabled="revealing" @click="revealSecrets">显示明文</button>。</template></p>
          <div class="extension-grid"><label class="field">连接超时（秒）<input v-model.number="form.startup_timeout_sec" class="input" type="number" min="1" max="300" /><span class="hint">最长 300，首次启动要现拉依赖的服务往大了填。</span></label><label class="field">工具超时（秒）<input v-model.number="form.tool_timeout_sec" class="input" type="number" min="1" max="900" /><span class="hint">最长 900，构建、抓取这类慢工具才需要调高。</span></label></div>
          <label class="field">允许的工具（每行一个，留空全部）<textarea v-model="form.enabled_tools" class="input code-input" rows="2"></textarea></label>
          <label class="field">禁用的工具（每行一个）<textarea v-model="form.disabled_tools" class="input code-input" rows="2"></textarea></label>
          <p class="hint">测试连接会访问服务；stdio 会启动配置的本地进程。</p>
          <p v-if="tested" role="status">连接成功，发现 {{ discovered.length }} 个工具</p><ul v-if="discovered.length"><li v-for="name in discovered" :key="name" class="tool-name">{{ name }}</li></ul>
        </template>
        <!-- 给谁用是这台机器人上的事，和上面那份全局配置分开写清楚。停用在列表
             那一行的开关上，这里只决定「开着的时候给谁」。 -->
        <template v-if="permissionItem">
          <hr class="form-divider" />
          <div class="field">
            <span>本机器人权限<template v-if="!permissionItem.enabled"> · 当前已停用</template></span>
            <div class="segmented" role="group" aria-label="开放档位">
              <button v-for="tier in openTiers" :key="tier.value" type="button" :class="{active: currentState(permissionItem) === tier.value}" :disabled="busy === permissionItem.id || !permissionItem.enabled" :title="tier.hint" @click="setState(permissionItem, tier.value)">{{ tier.label }}</button>
            </div>
            <span class="hint">{{ permissionItem.enabled ? '不用它就在列表里关掉那个开关；这里只决定开着的时候给谁用。' : '这台机器人已经停用它，先在列表里打开开关再设档位。' }}</span>
          </div>
          <template v-if="isOpenTier(permissionItem)">
            <div class="field"><label for="extension-access-users">开放对象 · 用户</label><IdChipInput input-id="extension-access-users" :model-value="accessUsers" placeholder="填账号后回车，留空 = 所有群成员" :resolve-names="resolveAccountNames" @update:model-value="accessUsers = $event" /></div>
            <div class="field"><label for="extension-access-groups">开放对象 · 群号</label><IdChipInput input-id="extension-access-groups" :model-value="accessGroups" placeholder="填群号后回车，留空 = 不限群" @update:model-value="accessGroups = $event" /></div>
            <p class="hint">两个都留空 = 这一档的所有人。都填则要同时满足：名单里的人，且只在这些群里。主人不受名单限制。</p>
            <p v-if="accessError" class="error-text" role="alert">{{ accessError }}</p>
            <button class="btn" :disabled="savingAccess" @click="saveAudience"><Save :size="15" />保存开放对象</button>
          </template>
        </template>
        <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      </div>
      <template #footer><button v-if="kind==='mcp'&&!readonly" class="btn" :disabled="saving" @click="resetForm"><RotateCcw :size="15" />恢复默认</button><button v-if="editPresetVerifiable" class="btn" :disabled="saving||verifying" @click="verifyEditPreset"><KeyRound :size="15" />检测令牌</button><button v-if="kind==='mcp'&&(!editPreset||editAdvanced)" class="btn" :disabled="saving" @click="testConnection"><PlugZap :size="15" />测试连接</button><button class="btn" :disabled="saving" @click="closeEditor">关闭</button><button v-if="!readonly" class="btn primary" :disabled="saving" @click="save"><Save :size="15" />保存</button></template>
    </Modal>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { ExternalLink, KeyRound, LayoutGrid, Plus, RefreshCw, RotateCcw, Rows3, Search, Settings2, Trash2, Save, PlugZap } from '@lucide/vue';
import Modal from './Modal.vue';
import { botScope } from '../bot-scope';
import { extensionLayout, setExtensionLayout } from '../extension-layout';
import { fetchAssistantUserNames, listMCPPresets, listManagedExtensions, manageExtension, type MCPPreset, type ManagedExtension } from '../api';
import IdChipInput from './IdChipInput.vue';
import { askConfirm } from '../confirm';
import { toastError, toastSuccess } from '../toast';

const props=defineProps<{kind:'skill'|'mcp'}>();
// 搜索、状态筛选、排列方式都照插件页那一套；排列方式是同一份设置，翻标签不会变。
const query=ref(''),status=ref<'all'|'on'|'off'>('all');
const layout=extensionLayout,setLayout=setExtensionLayout;
const matches=(text:string)=>{const q=query.value.trim().toLowerCase();return !q||text.toLowerCase().includes(q)};
const items=ref<ManagedExtension[]>([]),loading=ref(false),loadError=ref(''),busy=ref(''),editing=ref(false),existing=ref(false),readonly=ref(false),saving=ref(false),error=ref('');
const fromURL=ref(false),transport=ref<'http'|'stdio'>('http'),tested=ref(false),discovered=ref<string[]>([]),headers=ref('{}'),env=ref('{}');
const blank=()=>({name:'',content:'',source_url:'',url:'',command:'',args:'',cwd:'',enabled:true,startup_timeout_sec:20,tool_timeout_sec:60,enabled_tools:'',disabled_tools:''});
const form=ref(blank());
const secrets=computed({get:()=>transport.value==='http'?headers.value:env.value,set:v=>{if(transport.value==='http')headers.value=v;else env.value=v}});
// 从预设装出来的那条，编辑时还给它那张表；editAdvanced 是切回通用表单的后门。
const editPreset=ref<MCPPreset|null>(null),editPresetTransport=ref(''),editPresetValues=ref<Record<string,string>>({}),editAdvanced=ref(false),verifying=ref(false),verifyNote=ref('');
// 凭据读出来只有掩码；主人要看原文点「显示明文」，走单独的 reveal，Agent 那边没有这条路。
const editPresetMasks=ref<Record<string,string>>({}),storedSecrets=ref(false),revealed=ref(false),revealing=ref(false);
const editPresetTransportInfo=computed(()=>editPreset.value?.transports.find(t=>t.id===editPresetTransport.value));
const editPresetFields=computed(()=>editPresetTransportInfo.value?.fields||[]);
const editPresetTransportLabel=computed(()=>editPresetTransportInfo.value?.hint||editPresetTransportInfo.value?.label||'');
const editPresetVerifiable=computed(()=>!!(editing.value&&editPreset.value&&!editAdvanced.value&&!readonly.value&&editPresetTransportInfo.value?.verifiable));
const snapshot=ref('');
const state=()=>JSON.stringify([form.value,transport.value,headers.value,env.value,fromURL.value,editPresetValues.value,editAdvanced.value]);
let generation=0;
async function load(){const current=++generation;loading.value=true;loadError.value='';try{const result=await listManagedExtensions(botScope.value);if(current===generation)items.value=result.items.filter(i=>i.kind===props.kind)}catch(e){if(current===generation)loadError.value=String(e instanceof Error?e.message:e)}finally{if(current===generation)loading.value=false}
 // 还没添加的预设也要在列表里占一行，这份清单得跟着刷新。
 if(props.kind==='mcp')await refreshPresets()}
function openNew(){form.value=blank();headers.value=env.value='{}';editPresetMasks.value={};storedSecrets.value=revealed.value=false;fromURL.value=false;transport.value='http';existing.value=readonly.value=false;error.value='';tested.value=false;discovered.value=[];editPreset.value=null;editPresetTransport.value='';editPresetValues.value={};editAdvanced.value=false;verifyNote.value='';permissionName.value='';loadAudienceInputs(null);editing.value=true;snapshot.value=state()}
async function edit(item:ManagedExtension){try{const data=await manageExtension<any>({operation:'read',kind:props.kind,name:item.name});openNew();existing.value=true;readonly.value=!item.managed;form.value.name=item.name;permissionName.value=item.name;loadAudienceInputs(item);if(props.kind==='skill')form.value.content=data.content;else{const c=data.config;Object.assign(form.value,c,{args:(c.args||[]).join('\n'),enabled_tools:(c.enabled_tools||[]).join('\n'),disabled_tools:(c.disabled_tools||[]).join('\n'),enabled:c.enabled!==false});transport.value=c.command?'stdio':'http';storedSecrets.value=!!((data.configured_headers||[]).length||(data.configured_env||[]).length||String(c.url||'').includes('****'));headers.value=JSON.stringify(c.headers||{},null,2);env.value=JSON.stringify(c.env||{},null,2);if(data.preset){const known=await ensurePresets();editPreset.value=known.find(p=>p.id===data.preset)||null;editPresetTransport.value=data.preset_transport||'';editPresetValues.value={...(data.preset_values||{})};editPresetMasks.value={...(data.preset_secret_masks||{})};editAdvanced.value=!editPreset.value}}snapshot.value=state()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
// 只替换还是掩码或留空的那些：已经改过的值是人刚填的，不能被旧值盖回去。
function fillRevealed(raw:string,plain:Record<string,string>){const current:Record<string,string>=stringMap(raw);for(const [key,value] of Object.entries(current)){if(key in plain&&(value===''||value.includes('****')))current[key]=plain[key]}return JSON.stringify(current,null,2)}
async function revealSecrets(){revealing.value=true;try{const clean=snapshot.value===state();const data=await manageExtension<{url?:string;headers?:Record<string,string>;env?:Record<string,string>;preset_values?:Record<string,string>;preset_secrets?:Record<string,string>}>({operation:'reveal',kind:'mcp',name:form.value.name});
 // 地址里的查询参数、userinfo 也可能是令牌，读出来同样是掩码，一并换回原文。
 if(data.url&&form.value.url.includes('****'))form.value.url=data.url;
 for(const [key,value] of Object.entries(data.preset_values||{}))if((editPresetValues.value[key]||'').includes('****'))editPresetValues.value[key]=value;
 headers.value=fillRevealed(headers.value,data.headers||{});env.value=fillRevealed(env.value,data.env||{});
 for(const [key,value] of Object.entries(data.preset_secrets||{}))if(!editPresetValues.value[key])editPresetValues.value[key]=value;
 revealed.value=true;if(clean)snapshot.value=state()}catch(e){toastError(String(e instanceof Error?e.message:e))}finally{revealing.value=false}}
async function closeEditor(){if(!editing.value||saving.value)return;if(!readonly.value&&snapshot.value!==state()&&!await askConfirm({title:'放弃未保存的修改？',message:'本次编辑尚未保存。',confirmLabel:'放弃'}))return;editing.value=false}
async function importFile(event:Event){const file=(event.target as HTMLInputElement).files?.[0];if(!file)return;if(file.size>2*1024*1024){error.value='文件不能超过 2 MB';return}form.value.content=await file.text();fromURL.value=false}
const lines=(s:string)=>s.split('\n').map(x=>x.trim()).filter(Boolean);
function stringMap(raw:string){const result=JSON.parse(raw||'{}');if(!result||Array.isArray(result)||typeof result!=='object'||Object.values(result).some(v=>typeof v!=='string'))throw new Error('凭据必须是字符串键值 JSON 对象');return result}
function payload(operation:string){return {operation,kind:props.kind,name:form.value.name,replace:existing.value,content:fromURL.value?'':form.value.content,source_url:fromURL.value?form.value.source_url:'',config:{enabled:form.value.enabled,url:transport.value==='http'?form.value.url:'',command:transport.value==='stdio'?form.value.command:'',args:transport.value==='stdio'?lines(form.value.args):[],cwd:transport.value==='stdio'?form.value.cwd:'',headers:transport.value==='http'?stringMap(headers.value):{},env:transport.value==='stdio'?stringMap(env.value):{},startup_timeout_sec:form.value.startup_timeout_sec,tool_timeout_sec:form.value.tool_timeout_sec,enabled_tools:lines(form.value.enabled_tools),disabled_tools:lines(form.value.disabled_tools)}}}
// 「恢复默认」= 把这张表清回新建 MCP 的样子。只动表单，保存了才落盘——改废了
// 想重来的时候，比一个个字段往回删省事。
async function resetForm(){
 if(!await askConfirm({title:'把这张表恢复成默认？',message:'地址、命令、参数、凭据和工具名单都会清空，超时回到默认值。保存后才真正生效，点「关闭」就当没改过。',confirmLabel:'恢复默认'}))return;
 const name=form.value.name;form.value=blank();form.value.name=name;headers.value=env.value='{}';transport.value=editPreset.value&&!editAdvanced.value?transport.value:'http';editPresetValues.value={};tested.value=false;discovered.value=[];verifyNote.value='';error.value=''}
// 只在服务端真的验过时才提凭据：有的接法验不了，不能替它吹。
const presetVerifiedSuffix=(r:{account?:string;verified?:boolean})=>r.account?`（令牌对应账号 ${r.account}）`:r.verified?'（令牌已验证）':'';
const presetSavedMessage=(r:{account?:string;verified?:boolean})=>r.account?`已保存，令牌对应账号 ${r.account}`:r.verified?'已保存，令牌已验证，后续会话生效':'扩展已保存，后续会话生效';
function presetPayload(operation:string){return {operation,kind:'mcp',name:form.value.name,replace:true,preset:editPreset.value?.id,transport:editPresetTransport.value,values:editPresetValues.value,config:{enabled:form.value.enabled}}}
async function save(){saving.value=true;error.value='';try{
 // 预设那张表只回传它管的字段，剩下的（超时、工具名单）由服务端沿用已保存的。
 if(editPreset.value&&!editAdvanced.value){const result=await manageExtension<{account?:string;warning?:string;verified?:boolean}>(presetPayload('save'));editing.value=false;if(result.warning)toastError(result.warning);else toastSuccess(presetSavedMessage(result))}
 else{await manageExtension(payload('save'));editing.value=false;toastSuccess('扩展已保存，后续会话生效')}
 await load()}catch(e){error.value=String(e instanceof Error?e.message:e)}finally{saving.value=false}}
// 令牌对不对，保存前就问一次 Gitea。gitea-mcp 的握手不碰令牌，光看「测试连接」
// 是绿的说明不了什么。
async function runVerify(input:Record<string,unknown>,report:(message:string)=>void){verifying.value=true;verifyNote.value='';try{const result=await manageExtension<{verified:boolean;supported:boolean;account?:string;message?:string}>(input);verifyNote.value=result.verified?`令牌可用${result.account?`，对应账号 ${result.account}`:''}`:(result.message||'这条接法没法提前验凭据')}catch(e){report(String(e instanceof Error?e.message:e))}finally{verifying.value=false}}
const verifyEditPreset=()=>runVerify(presetPayload('verify'),message=>{error.value=message});
const verifyPreset=()=>runVerify({operation:'verify',kind:'mcp',name:presetName.value,preset:preset.value?.id,transport:presetTransport.value,values:presetValues.value},message=>{presetError.value=message});
async function testConnection(){saving.value=true;error.value='';tested.value=false;discovered.value=[];try{const result=await manageExtension<{connected:boolean;tools:string[]}>(payload('test'));tested.value=result.connected;discovered.value=result.tools}catch(e){error.value=String(e instanceof Error?e.message:e)}finally{saving.value=false}}
// 预设：服务端给字段清单，这里只负责渲染和回填，拼配置仍在服务端做。
const presetsOpen=ref(false),presets=ref<{preset:MCPPreset;installed:boolean;hidden?:boolean}[]>([]),preset=ref<MCPPreset|null>(null),presetTransport=ref(''),presetValues=ref<Record<string,string>>({}),presetName=ref(''),presetSaving=ref(false),presetError=ref('');
const presetFields=computed(()=>preset.value?.transports.find(t=>t.id===presetTransport.value)?.fields||[]);
const presetTransportHint=computed(()=>preset.value?.transports.find(t=>t.id===presetTransport.value)?.hint||'');
const presetVerifiable=computed(()=>!!(preset.value?.transports.find(t=>t.id===presetTransport.value)?.verifiable));
// 编辑已装好的服务时也要这份清单（拿字段定义），所以取一次存着。
async function ensurePresets(){if(!presets.value.length)await refreshPresets();return presets.value.map(entry=>entry.preset)}
async function refreshPresets(){try{presets.value=(await listMCPPresets()).items}catch{/* 取不到就少这几行，不拖累已装的服务 */}}
// 列表里那一行直接进预设表单，不用先打开一个目录弹窗。开关是「去添加」的入口，
// 点完要弹回去：没填完凭据它就还没装上，让它停在打开状态是在撒谎。
function startPreset(value:MCPPreset,event?:Event){if(event)(event.target as HTMLInputElement).checked=false;presetsOpen.value=true;pickPreset(value)}
// 还没配的内置预设：列表里照样占一行，装上之后这一行就变成真正的那条服务。
const pendingPresets=computed(()=>props.kind==='mcp'?presets.value.filter(entry=>!entry.hidden&&!entry.installed&&!items.value.some(item=>item.name===entry.preset.name)):[]);
const searchedItems=computed(()=>items.value.filter(item=>matches(`${item.name} ${item.description||''} ${item.source||''}`)));
const enabledCount=computed(()=>searchedItems.value.filter(item=>item.enabled).length);
const visibleItems=computed(()=>status.value==='all'?searchedItems.value:searchedItems.value.filter(item=>item.enabled===(status.value==='on')));
// 还没配凭据的预设既不算启用也不算停用，只在「全部」里出现。
const visiblePresets=computed(()=>status.value!=='all'?[]:pendingPresets.value.filter(entry=>matches(`${entry.preset.title} ${entry.preset.name} ${entry.preset.summary}`)));
const statusFilters=computed(()=>[
 {value:'all' as const,label:'全部',count:searchedItems.value.length+visiblePresets.value.length},
 {value:'on' as const,label:'已启用',count:enabledCount.value},
 {value:'off' as const,label:'已停用',count:searchedItems.value.length-enabledCount.value},
]);
const hiddenPresets=computed(()=>props.kind==='mcp'?presets.value.filter(entry=>entry.hidden):[]);
// 删掉的是列表里那一行，不是服务：随时能放回来，所以底下留一句找得回来的话。
async function hidePreset(value:MCPPreset){if(!await askConfirm({title:`从列表里去掉 ${value.title}？`,message:'只是不再显示这一行，随时可以在下面「显示隐藏的预设」里放回来。已经配好的服务不受影响。',confirmLabel:'去掉'}))return;
 try{await manageExtension({operation:'presets',action:'hide',kind:'mcp',preset:value.id});await refreshPresets()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
async function showHiddenPresets(){try{await Promise.all(hiddenPresets.value.map(entry=>manageExtension({operation:'presets',action:'show',kind:'mcp',preset:entry.preset.id})));await refreshPresets()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
function closePresets(){if(presetSaving.value)return;presetsOpen.value=false;preset.value=null}
function pickPreset(value:MCPPreset){preset.value=value;presetTransport.value=value.transports[0]?.id||'';presetValues.value={};presetName.value=items.value.some(i=>i.name===value.name)?`${value.name}-2`:value.name;presetError.value='';verifyNote.value=''}
async function savePreset(){if(!preset.value)return;presetSaving.value=true;presetError.value='';try{const result=await manageExtension<{account?:string;warning?:string;verified?:boolean}>({operation:'save',kind:'mcp',name:presetName.value,preset:preset.value.id,transport:presetTransport.value,values:presetValues.value});presetsOpen.value=false;preset.value=null;if(result.warning)toastError(result.warning);else toastSuccess(`已添加${presetVerifiedSuffix(result)}，默认仅主人可用，在它那张卡片的「设置」里开放`);await load()}catch(e){presetError.value=String(e instanceof Error?e.message:e)}finally{presetSaving.value=false}}
// 权限跟着正在编辑的那一条走：设置弹窗里改，改完从列表里取回最新的一份。
const permissionName=ref(''),accessUsers=ref<string[]>([]),accessGroups=ref<string[]>([]),accessError=ref(''),savingAccess=ref(false);
const permissionItem=computed(()=>editing.value&&botScope.value&&permissionName.value?items.value.find(i=>i.kind===props.kind&&i.name===permissionName.value)||null:null);
async function resolveAccountNames(ids:string[]):Promise<Record<string,string>>{const response=await fetchAssistantUserNames(ids);return response.names??{}}
const extensionStates=[{value:'off',label:'停用',hint:'这台机器人不用它'},{value:'owner',label:'仅主人',hint:'只有主人会话能用'},{value:'admins',label:'群管',hint:'群主和群管理员也能用；平台给不出身份时按普通成员处理'},{value:'members',label:'群成员',hint:'群成员也能用，可再限定对象'}] as const;
type ExtensionState=typeof extensionStates[number]['value'];
// 「停用」是列表那一行的开关，弹窗里只挑「开着的时候给谁」。
const openTiers=extensionStates.filter(state=>state.value!=='off');
const tierLabel=(item:ManagedExtension)=>extensionStates.find(state=>state.value===currentState(item))?.label||'';
// 开关只管启用与否：成员档和名单原样留着，关掉再打开还是原来那一档。
async function toggleEnabled(item:ManagedExtension){const profile=botScope.value;if(!profile&&props.kind!=='mcp')return;
 // 全局这一下会把每台机器人单独的开关都清掉，统一跟随，所以先问一句。
 if(!profile&&!await askConfirm({title:`所有机器人${item.enabled?'停用':'启用'} ${item.name}？`,message:`每台机器人单独设过的开关都会被覆盖，统一${item.enabled?'停用':'启用'}。之后在某台机器人上单独${item.enabled?'打开':'关掉'}，那台照样按它自己的来。`,confirmLabel:item.enabled?'全部停用':'全部启用'}))return;
 busy.value=item.id;
 try{const result=await manageExtension<{warning?:string}>({operation:'enabled',kind:props.kind,name:item.name,profile_id:profile||undefined,enabled:!item.enabled});if(result?.warning)toastError(result.warning);await load()}
 catch(e){toastError(String(e instanceof Error?e.message:e));await load()}finally{busy.value=''}}
const currentState=(item:ManagedExtension):ExtensionState=>!item.enabled?'off':!item.members_enabled?'owner':item.member_audience?.min_role==='admin'?'admins':'members';
const isOpenTier=(item:ManagedExtension)=>{const state=currentState(item);return state==='members'||state==='admins'};
// 小字只写「被收窄成什么样」，没收窄就不写：档位那一段已经说过谁能用了。
function audienceSummary(item:ManagedExtension){if(!isOpenTier(item))return '';const users=item.member_audience?.users?.length||0,groups=item.member_audience?.groups?.length||0;if(!users&&!groups)return '';return `限定 ${[users?`${users} 人`:'',groups?`${groups} 群`:''].filter(Boolean).join(' · ')}`}
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
  if((state==='off')!==!item.enabled){const result=await manageExtension<{warning?:string}>({...base,operation:'enabled',enabled:state!=='off'});if(result?.warning)toastError(result.warning)}
  if(members&&!item.members_enabled)await manageExtension({...base,operation:'members',enabled:true});
  await load();
 }catch(e){toastError(String(e instanceof Error?e.message:e));await load()}finally{busy.value=''}}
function loadAudienceInputs(item:ManagedExtension|null){accessUsers.value=[...(item?.member_audience?.users||[])];accessGroups.value=[...(item?.member_audience?.groups||[])];accessError.value=''}
async function saveAudience(){const item=permissionItem.value,profile=botScope.value;if(!item||!profile)return;savingAccess.value=true;accessError.value='';try{await manageExtension({operation:'audience',kind:props.kind,name:item.name,profile_id:profile,audience:{min_role:item.member_audience?.min_role||'',users:accessUsers.value,groups:accessGroups.value}});toastSuccess('开放对象已更新，后续会话生效');await load()}catch(e){accessError.value=String(e instanceof Error?e.message:e)}finally{savingAccess.value=false}}
async function remove(item:ManagedExtension){if(!await askConfirm({title:`删除 ${item.name}？`,message:'全局删除会影响使用它的所有机器人。',confirmLabel:'删除',danger:true}))return;try{await manageExtension({operation:'delete',kind:props.kind,name:item.name});await load()}catch(e){toastError(String(e instanceof Error?e.message:e))}}
watch(() => state(),()=>{tested.value=false;discovered.value=[]});
async function prepareLeave(){await closeEditor();return !editing.value}
defineExpose({prepareLeave});
watch(botScope,load);onMounted(load);
</script>

<style scoped>
.extension-manager{padding-top:20px}.preset-row{display:flex;align-items:center;gap:12px;padding:12px 0;border-top:1px solid var(--border)}.extension-list{margin-top:4px}.extension-info{flex:1;min-width:0;overflow-wrap:anywhere}.extension-info p{margin:6px 0;color:var(--muted)}.extension-info small{color:var(--muted)}.extension-form{display:grid;gap:14px}/* 按钮也是 inline-flex，作为 grid 项同样会被拉满一行：这里按内容宽度靠左。 */
.extension-form>.btn{justify-self:start}.code-input{font-family:monospace;resize:vertical;min-width:0;white-space:pre-wrap}.extension-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.extension-info strong{display:flex;align-items:center;gap:8px}.form-divider{border:0;border-top:1px solid var(--border);margin:4px 0 0}.extension-audience-note{color:var(--text-secondary)}.tool-name{overflow-wrap:anywhere}.error-text{color:var(--danger)}.extension-doc-link{display:inline-flex;align-items:center;gap:4px;color:var(--accent);font-size:11.5px;text-decoration:underline;text-underline-offset:2px}.extension-doc-link:hover{color:var(--accent-strong)}.link-button{background:none;border:0;padding:0;color:var(--accent);font:inherit;cursor:pointer;text-decoration:underline}@media(max-width:600px){.extension-grid{grid-template-columns:1fr}}
</style>
