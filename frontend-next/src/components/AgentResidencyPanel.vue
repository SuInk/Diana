<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 参照 VS Code 的工具选择器和 Claude Code 的 /context：一行一项、一个勾选框，
       按来源分组可折叠，每一项都带数字。以前是一行一张卡片外加一个按钮，一屏只放得下
       三四项，按需的还得点进弹窗才看得到。 -->
  <section class="rz">
    <header class="rz-head">
      <div>
        <h3>每轮带上的工具和 Skill</h3>
        <span class="rz-sub">勾上的每轮都带完整定义（Skill 带正文）；没勾的只在目录里占一行，模型用到时自己加载。</span>
      </div>
      <button v-if="profileID && items.length" class="btn small" type="button" :aria-expanded="panelOpen" @click="panelOpen = !panelOpen">
        {{ panelOpen ? "收起" : "展开名单" }}
      </button>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading" class="hint">正在读取…</p>
    <p v-else-if="!profileID" class="hint">先保存这台机器人，再回来改名单。</p>
    <template v-else>
      <p v-if="!items.length" class="hint">这台机器人还没跑过一轮对话。工具目录是按平台、权限和插件开关逐轮拼出来的，跑过一次之后这里才知道它实际有哪些工具。</p>
      <template v-else>
        <!-- 开销条：每轮固定要付的这笔钱分成两截，改动没保存时直接在数字上显示差额。 -->
        <div class="rz-meter">
          <div class="rz-bar" role="img" :aria-label="`常驻定义 ${summary.residentTokens} token，按需目录 ${summary.deferredTokens} token`">
            <span class="rz-bar-on" :style="{width: barPercent(summary.residentTokens)}"></span>
            <span class="rz-bar-off" :style="{width: barPercent(summary.deferredTokens)}"></span>
          </div>
          <dl class="rz-stats">
            <div><dt><i class="rz-dot on"></i>常驻定义</dt><dd class="mono">{{ formatTokens(summary.residentTokens) }}<small class="rz-delta" :class="[delta > 0 ? 'up' : 'down', {idle: !delta}]">{{ delta ? (delta > 0 ? '+' : '−') + formatTokens(Math.abs(delta)) : '+0 tok' }}</small></dd></div>
            <div><dt><i class="rz-dot off"></i>按需目录</dt><dd class="mono">{{ formatTokens(summary.deferredTokens) }}</dd></div>
            <div><dt>工具</dt><dd class="mono">{{ summary.resident }}<span>/{{ summary.resident + summary.deferred }}</span> 常驻</dd></div>
            <div v-if="skills.length"><dt>Skill 正文</dt><dd class="mono">{{ residentSkills.length }}<span>/{{ skills.length }}</span> 常驻</dd></div>
          </dl>
          <!-- 待保存状态占一行固定高度，常在：以前是改动后才插在列表上方的提示条，一出现就把
               整个列表往下推，勾一下东西就跳一下。 -->
          <p class="rz-status" :class="{dirty}" role="status" :title="statusTitle">
            <span class="rz-status-text">{{ statusText }}</span>
            <button v-if="dirty" class="link-button" type="button" @click="discard">放弃</button>
          </p>
        </div>

        <!-- 名单默认收起：开销条就是这张卡要回答的问题（每轮付多少），名单是要改的时候
             才翻的。改了没保存时自动展开，免得改动藏在收起的名单里。 -->
        <template v-if="panelOpen || dirty">
        <div class="rz-toolbar">
          <input v-model="query" class="input rz-search" type="search" placeholder="搜索名称、说明或工具名" aria-label="搜索工具和 Skill" />
          <div class="segmented" role="group" aria-label="筛选">
            <button v-for="option in filters" :key="option.value" type="button" :class="{active: filter === option.value}" @click="filter = option.value">{{ option.label }}</button>
          </div>
          <button class="link-button rz-expand" type="button" @click="toggleAll">{{ allOpen ? '全部收起' : '全部展开' }}</button>
          <button class="link-button" type="button" :disabled="!savedList && !dirty" @click="resetAll">恢复推荐名单</button>
        </div>

        <section v-for="section in sections" :key="section.kind" class="rz-group">
          <h4><span>{{ section.label }}</span><small>{{ section.on }}/{{ section.total }} 常驻<template v-if="section.tokens"> · 每轮 {{ formatTokens(section.tokens) }}</template></small></h4>
          <ul class="rz-list">
            <template v-for="row in section.rows" :key="row.id">
              <li class="rz-row" :class="{changed: rowChanged(row)}">
                <button v-if="childrenOf(row).length || hasDetail(row)" type="button" class="rz-caret" :class="{open: isOpen(row)}" :aria-expanded="isOpen(row)" :aria-label="`${isOpen(row) ? '收起' : '展开'} ${row.name}`" @click="opened[row.id] = !isOpen(row)">›</button>
                <span v-else class="rz-caret"></span>
                <input type="checkbox" :checked="rowOn(row)" :indeterminate.prop="rowPartial(row)" :aria-label="`${row.name} 常驻`" @change="toggleRow(row)" />
                <span class="rz-main" :title="row.detail || row.description">
                  <strong>{{ row.name }}</strong>
                  <span v-if="row.stale" class="rz-desc">重启后还没跑过对话，说明和开销暂缺；名单仍然生效</span>
                  <span v-else-if="!(isOpen(row) && hasDetail(row))" class="rz-desc">{{ row.description }}</span>
                </span>
                <span class="rz-meta">
                  <span v-if="childrenOf(row).length" class="rz-count">{{ childrenOf(row).filter(isResident).length }}/{{ childrenOf(row).length }}</span>
                  <span class="rz-cost mono" :title="`常驻 ${formatTokens(residentCost(row))} · 按需只占目录 ${formatTokens(deferredCost(row))}`">{{ formatTokens(rowCost(row)) }}</span>
                </span>
              </li>
              <template v-if="isOpen(row)">
                <li v-if="hasDetail(row)" class="rz-detail">{{ row.detail }}</li>
                <li v-for="child in childrenOf(row)" :key="child.id" class="rz-row rz-child" :class="{changed: leafChanged(child)}">
                  <span class="rz-caret"></span>
                  <input type="checkbox" :checked="isResident(child)" :aria-label="`${child.name} 常驻`" @change="isResident(child) ? remove(child) : add(child)" />
                  <span class="rz-main wrap"><strong>{{ child.name }}</strong><span class="rz-desc">{{ child.detail || child.description }}</span></span>
                  <span class="rz-meta"><span class="rz-cost mono">{{ formatTokens(isResident(child) ? residentCost(child) : deferredCost(child)) }}</span></span>
                </li>
              </template>
            </template>
          </ul>
        </section>

        <!-- Skill 同一种勾选；它多出来的那一档（带触发词的默认命中才带正文）只有部分 Skill 有，
             放在行尾就地切换，不另占一列。 -->
        <section v-if="visibleSkills.length" class="rz-group">
          <h4><span>Skill</span><small>{{ residentSkills.length }}/{{ skills.length }} 常驻正文</small></h4>
          <ul class="rz-list">
            <li v-for="skill in visibleSkills" :key="skill.id" class="rz-row" :class="{changed: skillPending[skill.id] !== undefined}">
              <span class="rz-caret"></span>
              <input type="checkbox" :checked="skillTier(skill) === true" :aria-label="`${skill.name} 常驻正文`" @change="setSkillTier(skill, skillTier(skill) === true ? null : true)" />
              <span class="rz-main" :title="skill.description">
                <strong>{{ skill.name }}</strong>
                <span class="rz-desc">{{ skill.description || skill.source || '本地 Skill' }}</span>
              </span>
              <span class="rz-meta">
                <template v-if="skillTier(skill) !== true && skill.keywords?.length">
                  <button class="rz-chip" :class="{off: skillTier(skill) === false}" type="button" :title="skillTier(skill) === false ? '现在命中触发词也不带正文，点一下恢复' : '最近两条消息命中这些词时自动带正文，点一下关掉'" @click="setSkillTier(skill, skillTier(skill) === false ? null : false)">{{ skillTier(skill) === false ? '触发已关' : `触发：${skill.keywords.join('、')}` }}</button>
                </template>
                <span class="rz-cost">{{ skillTier(skill) === true ? '正文' : '目录' }}</span>
              </span>
            </li>
          </ul>
        </section>

        <p v-if="!sections.length && !visibleSkills.length" class="hint">没有匹配的项。</p>
        </template>
        <p class="hint rz-foot">开销按实际发给模型的工具定义估算，不含系统提示词和历史消息；数字取自这台机器人最近一轮回复。</p>
      </template>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { listAgentResidency, listManagedExtensions, manageExtension, saveAgentResidencyList, type AgentResidencyEntry, type ManagedExtension } from '../api';

// 档位跟着「正在编辑的这台机器人」走，不跟顶栏的全局作用域：在机器人配置页里改
// 的就是眼前这一台，两者不一致时按全局作用域取数会改错机器人。
const props = defineProps<{profile?: string}>();
const profileID = computed(() => props.profile ?? '');

type Tier = boolean | null;
const items = ref<AgentResidencyEntry[]>([]), loading = ref(false), loadError = ref('');
// savedList 是「这台机器人有没有自己的名单」。没有就跟着内置推荐走——这时候一个
// 工具不在名单里不代表不常驻，它可能正被推荐带着。
const savedList = ref(false);
const query = ref('');
const filter = ref<'all' | 'on' | 'off'>('all');
const filters = [{value: 'all', label: '全部'}, {value: 'on', label: '常驻'}, {value: 'off', label: '按需'}] as const;
// 名单整体默认收起（panelOpen），展开后每一行也默认收起：一行一个名字加一句说明，
// 一屏能看全；要看插件旗下的工具或完整说明再点那一行，或者「全部展开」。单独点过的
// 行记在 opened 里，「全部展开 / 收起」会把它们清掉。
const panelOpen = ref(false);
const opened = reactive<Record<string, boolean>>({});
const allOpen = ref(false);
const isOpen = (row: AgentResidencyEntry) => opened[row.id] ?? allOpen.value;
function toggleAll() {
  allOpen.value = !allOpen.value;
  for (const id of Object.keys(opened)) delete opened[id];
}
// 改动先攒在这里再一起提交。每点一下就发一次请求，等于用户调十项就把所有会话的
// 前缀缓存作废十次——这一页自己写着那句代价，交互不能跟它对着干。
const pending = reactive<Record<string, Tier>>({});
// reset 是「退回推荐名单」：它不是把每一项都改一遍，而是把这台机器人的名单整个撤掉，
// 之后继续跟着版本走。所以它得单独记一笔，不能混在逐项改动里。
const reset = ref(false);

// 底下仍是三态（没配 / 常驻 / 按需），但界面不摆出来：用户要的是一份名单，加进来
// 或者拿出去。默认在名单里的点「移除」写成按需，不在的点「添加」写成常驻，「没配过」
// 只是它们的初始状态，不需要一个按钮专门表达。
const saved = (item: AgentResidencyEntry): Tier => item.resident === undefined ? null : item.resident;
// 按下「恢复推荐名单」之后，这一屏就该当作没有名单来显示。
const listed = computed(() => savedList.value && !reset.value);
// 按下「恢复推荐名单」之后，屏幕上就该立刻显示推荐名单的样子，而不是已存的那一份。
const current = (item: AgentResidencyEntry): Tier => pending[item.id] !== undefined ? pending[item.id] : reset.value ? null : saved(item);
const byID = computed(() => new Map(items.value.map(item => [item.id, item])));
const childrenOf = (item: AgentResidencyEntry) => items.value.filter(child => child.parent === item.id);
// 工具没单独表过态就跟着它所属的插件或 MCP 服务走，那一条也没表态才落到推荐名单。
function fallback(item: AgentResidencyEntry): boolean {
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  const tier = parent ? current(parent) : null;
  if (tier !== null) return tier;
  // 有名单就以名单为准：没写进去就是不常驻。没名单才回到内置推荐。
  return listed.value ? false : item.default;
}
const isResident = (item: AgentResidencyEntry) => current(item) ?? fallback(item);
const hasDetail = (item: AgentResidencyEntry) => !!item.detail && item.detail !== item.description;
const residentCost = (item: AgentResidencyEntry) => item.resident_tokens || 0;
const deferredCost = (item: AgentResidencyEntry) => item.deferred_tokens || 0;
function formatTokens(value: number) {
  if (!value) return '0 tok';
  return value < 1000 ? `${value} tok` : `${(value / 1000).toFixed(1)}k tok`;
}

function add(item: AgentResidencyEntry) {
  setTier(item, true);
  // 整条加回来时，之前单独排除掉的那几个不跟着回来——那是用户明说过的话，不该被
  // 一次「添加」悄悄抹掉。要一起回来有「放回来」。
}
function remove(item: AgentResidencyEntry) {
  setTier(item, false);
}
function setTier(item: AgentResidencyEntry, value: Tier) {
  if (saved(item) === value && !reset.value) delete pending[item.id];
  else pending[item.id] = value;
  prunePending();
}
// 勾掉再勾回来，屏幕上的结果和已存的一样，但 pending 里可能留着一条「显式写成常驻」：
// 原来靠推荐名单常驻、现在变成自己写的，效果相同，却让「待保存」亮着、说不出改了什么。
// 逐条试着拿掉，拿掉后所有工具的结果都不变，就说明它是空改动。
function prunePending() {
  if (reset.value) return;
  const snapshot = () => leaves.value.map(isResident).join(',');
  const before = snapshot();
  for (const id of Object.keys(pending)) {
    const value = pending[id];
    delete pending[id];
    if (snapshot() !== before) pending[id] = value;
  }
}

const leaves = computed(() => items.value.filter(item => item.kind === 'tool'));
// 汇总只按工具算：分组行是容器，它的 token 是旗下工具的和，两边都算就重复了。
const summary = computed(() => leaves.value.reduce((total, item) => {
  if (isResident(item)) {
    total.resident += 1;
    total.residentTokens += residentCost(item);
  } else {
    total.deferred += 1;
    total.deferredTokens += deferredCost(item);
  }
  return total;
}, {resident: 0, deferred: 0, residentTokens: 0, deferredTokens: 0}));
// 已存下来的结果，用来和屏幕上的比出差额。口径和 fallback 一致，只是不看待保存的改动。
const savedResident = (item: AgentResidencyEntry): boolean => {
  const own = saved(item);
  if (own !== null) return own;
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  const inherited = parent ? saved(parent) : null;
  if (inherited !== null) return inherited;
  return savedList.value ? false : item.default;
};
const cost = (item: AgentResidencyEntry, resident: boolean) => resident ? residentCost(item) : deferredCost(item);
const delta = computed(() => leaves.value.reduce((sum, item) => sum + cost(item, isResident(item)) - cost(item, savedResident(item)), 0));
// 待保存的不是「点了几下」，而是「有几个工具的待遇会变」——用户关心的是后者，而且
// 一次插件级的增删本来就会带动好几个工具。
const dirtyCount = computed(() => leaves.value.filter(item => isResident(item) !== savedResident(item)).length);
// Skill 和工具在一页、一种操作，但底下仍是三态：#717 定过，Skill 不进工具名单——
// 编辑一次名单不该顺带把所有带触发词的 Skill 判成永不注入。所以它们单独攒、逐项写。
const skillItems = ref<ManagedExtension[]>([]);
const skillPending = reactive<Record<string, Tier>>({});
// 这台机器人停用的 Skill 根本不会下发，给它配档位没有意义。
const skills = computed(() => skillItems.value.filter(item => item.kind === 'skill' && item.enabled && item.available !== false).sort((a, b) => a.name.localeCompare(b.name)));
const savedSkillTier = (skill: ManagedExtension): Tier => skill.resident === undefined ? null : skill.resident;
const skillTier = (skill: ManagedExtension): Tier => skillPending[skill.id] !== undefined ? skillPending[skill.id] : savedSkillTier(skill);
const residentSkills = computed(() => skills.value.filter(skill => skillTier(skill) === true));
const skillDirtyCount = computed(() => Object.keys(skillPending).length);
function setSkillTier(skill: ManagedExtension, value: Tier) {
  if (savedSkillTier(skill) === value) delete skillPending[skill.id];
  else skillPending[skill.id] = value;
}
const dirty = computed(() => reset.value || dirtyCount.value > 0 || skillDirtyCount.value > 0);

// 一条插件或 MCP 服务整条在名单里时，它旗下的工具不再单独列一行：名单上的单位就是
// 用户加进来的那一个。整条不在名单里、其中某个工具被单独加进来了，那个工具自己上榜。
// 名单上的一行是「用户加进来的那一个」。整条插件或服务只有被明确加过才算一行；
// 没加过的时候，推荐名单是按工具推荐的，那就按工具列——否则一个插件会因为旗下
// 某个工具被推荐而整条上榜，再标一句「其中排除了另外两个」，说的全不是实情。
const addedWhole = (item: AgentResidencyEntry) => item.kind !== 'tool' && current(item) === true;
const onList = (item: AgentResidencyEntry) => {
  if (item.kind !== 'tool') return addedWhole(item);
  if (!isResident(item)) return false;
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  return !(parent && addedWhole(parent));
};
const byCost = (a: AgentResidencyEntry, b: AgentResidencyEntry) => residentCost(b) - residentCost(a) || a.name.localeCompare(b.name);
const residentRows = computed(() => items.value.filter(onList).sort(byCost));
const matches = (item: AgentResidencyEntry) => {
  const text = query.value.trim().toLowerCase();
  if (!text) return true;
  return `${item.name} ${item.description || ''} ${(item.tools || []).join(' ')}`.toLowerCase().includes(text);
};


// 一行的勾选状态。插件和 MCP 服务整条勾上就是整条进名单；只勾了其中几个工具时
// 显示成半选，和 VS Code 工具选择器里工具集的样子一致。
const rowOn = (row: AgentResidencyEntry) => {
  if (row.kind === 'tool') return isResident(row);
  const children = childrenOf(row);
  return addedWhole(row) || (children.length > 0 && children.every(isResident));
};
const rowPartial = (row: AgentResidencyEntry) => row.kind !== 'tool' && !rowOn(row) && childrenOf(row).some(isResident);
// 「改过」按实际结果算，不按点过没有：点了又点回来的不该还标着。
const leafChanged = (item: AgentResidencyEntry) => isResident(item) !== savedResident(item);
const rowChanged = (row: AgentResidencyEntry) => childrenOf(row).length ? childrenOf(row).some(leafChanged) : leafChanged(row);
const statusText = computed(() => {
  if (!dirty.value) return '没有未保存的改动';
  const parts: string[] = [];
  if (reset.value) parts.push('退回推荐名单');
  else if (dirtyCount.value) parts.push(`${dirtyCount.value} 个工具`);
  if (skillDirtyCount.value) parts.push(`${skillDirtyCount.value} 个 Skill`);
  return `未保存：${parts.join('、')}会变，点「保存配置」生效`;
});
const statusTitle = computed(() => dirty.value && (reset.value || dirtyCount.value) ? '保存那一下会让所有会话的工具列表变一次、缓存重算一轮' : '');
function toggleRow(row: AgentResidencyEntry) {
  if (row.kind === 'tool') {
    if (isResident(row)) remove(row);
    else add(row);
    return;
  }
  if (rowOn(row) || rowPartial(row)) {
    // 整条拿掉时，单独勾过的那几个也一起拿掉：用户点的是这一整行。
    remove(row);
    for (const child of childrenOf(row)) if (current(child) === true) setTier(child, null);
  } else {
    add(row);
  }
}
// 一行现在每轮实际花多少：常驻的按完整定义算，没常驻的按目录那一行算。整条插件或
// 服务按旗下工具逐个加，半选时才不会把没勾的那几个也算成常驻价。
const leafCost = (item: AgentResidencyEntry) => isResident(item) ? residentCost(item) : deferredCost(item);
const rowCost = (row: AgentResidencyEntry) => {
  const children = childrenOf(row);
  return children.length ? children.reduce((sum, child) => sum + leafCost(child), 0) : leafCost(row);
};
const filterOK = (on: boolean, partial = false) => filter.value === 'all' || (filter.value === 'on' ? on || partial : !on);
// 分组按来源排：内置工具、插件、MCP 服务。组内按开销从大到小，贵的排前面。
const sections = computed(() => {
  const top = items.value.filter(item => !item.parent && matches(item) && filterOK(rowOn(item), rowPartial(item))).sort(byCost);
  return [
    {kind: 'tool', label: '内置工具'},
    {kind: 'plugin', label: '插件'},
    {kind: 'mcp', label: 'MCP 服务'},
  ].map(section => {
    const all = items.value.filter(item => !item.parent && item.kind === section.kind);
    const rows = top.filter(row => row.kind === section.kind);
    const leavesOf = (row: AgentResidencyEntry) => childrenOf(row).length ? childrenOf(row) : [row];
    const tokens = all.flatMap(leavesOf).filter(isResident).reduce((sum, leaf) => sum + residentCost(leaf), 0);
    return {...section, rows, total: all.length, on: all.filter(rowOn).length, tokens};
  }).filter(section => section.rows.length);
});
const visibleSkills = computed(() => skills.value.filter(skill => {
  const text = query.value.trim().toLowerCase();
  if (text && !`${skill.name} ${skill.description || ''} ${(skill.keywords || []).join(' ')}`.toLowerCase().includes(text)) return false;
  return filterOK(skillTier(skill) === true);
}));
function barPercent(value: number) {
  const total = summary.value.residentTokens + summary.value.deferredTokens;
  return total ? `${(value / total) * 100}%` : '0%';
}

let generation = 0;
async function load() {
  const current = ++generation;
  loading.value = true;
  loadError.value = '';
  try {
    const [result, extensions] = await Promise.all([listAgentResidency(profileID.value), listManagedExtensions(profileID.value)]);
    if (current === generation) {
      items.value = result.items;
      savedList.value = !!result.listed;
      skillItems.value = extensions.items;
    }
  } catch (e) {
    if (current === generation) loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    if (current === generation) loading.value = false;
  }
}
// 「恢复推荐名单」只管工具：Skill 没有推荐名单，也不该被它一并清掉。
function resetAll() {
  for (const id of Object.keys(pending)) delete pending[id];
  reset.value = true;
}
function discard() {
  for (const id of Object.keys(pending)) delete pending[id];
  for (const id of Object.keys(skillPending)) delete skillPending[id];
  reset.value = false;
}
// 名单不自己保存：它和这一页的其他配置同属一台机器人，两个保存按钮会让人以为
// 点了其中一个就全存上了。宿主页面保存成功后调这里，写完再拉一次最新状态。
async function applyPending() {
  const profile = profileID.value;
  if (!profile || !dirty.value) return;
  // 存的是整份名单，一次写完：名单本来就是一整件事，分几次写中间会出现「插件进去了、
  // 单独拿掉的那个还没拿掉」这种谁都不想要的状态，而它每变一次，所有会话的工具列表
  // 就跟着变一次。
  if (reset.value || Object.keys(pending).length) {
    await saveAgentResidencyList(profile, reset.value ? null : residentRows.value.map(row => row.id));
  }
  for (const [id, value] of Object.entries(skillPending)) {
    const skill = skills.value.find(item => item.id === id);
    if (!skill) continue;
    const payload: Record<string, unknown> = {operation: 'residency', kind: 'skill', name: skill.name, profile_id: profile};
    if (value !== null) payload.resident = value;
    await manageExtension(payload);
  }
  discard();
  await load();
}
defineExpose({applyPending, pendingCount: dirtyCount, dirty, discard, reload: load});
watch(profileID, () => { discard(); load(); });
onMounted(load);
</script>

<style scoped>
.rz{display:flex;flex-direction:column;gap:12px}
.rz .hint{color:var(--muted);font-size:12px;margin:0}
.rz-head{display:flex;align-items:flex-start;justify-content:space-between;gap:10px;flex-wrap:wrap}.rz-head>div{flex:1 1 240px;min-width:0}.rz-head h3{margin:0;font-size:15px}.rz-sub{display:block;margin-top:4px;color:var(--muted);font-size:12.5px}
.rz-meter{display:flex;flex-direction:column;gap:8px;padding:12px 14px;border:1px solid var(--border);border-radius:var(--radius-sm)}
.rz-bar{display:flex;height:8px;border-radius:999px;overflow:hidden;background:var(--surface-2,rgba(127,127,127,.15))}
.rz-bar-on{background:var(--accent)}.rz-bar-off{background:color-mix(in srgb,var(--accent) 35%,transparent)}
/* 固定列宽：数字变长变短时，同一行后面几项不跟着左右挪。 */
.rz-stats{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:6px 24px;margin:0}
.rz-stats div{display:flex;align-items:baseline;gap:8px}.rz-stats dt{color:var(--muted);font-size:12px;display:flex;align-items:center;gap:6px}.rz-stats dd{margin:0;font-size:13px;color:var(--text)}
.rz-stats dd span{color:var(--muted)}.rz-stats small{margin-left:6px;font-size:11.5px}.rz-delta{display:inline-block;min-width:64px}.rz-delta.idle{visibility:hidden}.rz-stats small.up{color:var(--warn)}.rz-stats small.down{color:var(--ok,#3fb950)}
.rz-dot{display:inline-block;width:8px;height:8px;border-radius:2px}.rz-dot.on{background:var(--accent)}.rz-dot.off{background:color-mix(in srgb,var(--accent) 35%,transparent)}
.rz-toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.rz-search{flex:1;min-width:180px;max-width:320px}.rz-toolbar .rz-expand{margin-left:auto}
.rz-status{display:flex;align-items:center;gap:8px;height:20px;margin:0;font-size:12px;color:var(--muted);white-space:nowrap}.rz-status-text{overflow:hidden;text-overflow:ellipsis}.rz-status.dirty{color:var(--accent)}.rz-status .link-button{flex-shrink:0}
.rz-group h4{display:flex;align-items:baseline;justify-content:space-between;gap:12px;margin:4px 0 0;padding:0 2px 6px;border-bottom:1px solid var(--border);font-size:12.5px;color:var(--text-secondary)}
.rz-group h4 small{font-weight:400;color:var(--muted);font-variant-numeric:tabular-nums}
.rz-list{list-style:none;margin:0;padding:0}
.rz-row{display:grid;grid-template-columns:16px 18px minmax(0,1fr) auto;align-items:center;gap:8px;min-height:34px;padding:2px 4px;border-bottom:1px solid color-mix(in srgb,var(--border) 60%,transparent)}
.rz-row:hover{background:var(--surface-2,rgba(127,127,127,.06))}
.rz-row.changed{box-shadow:inset 2px 0 0 var(--accent)}
.rz-row input[type=checkbox]{margin:0;accent-color:var(--accent);cursor:pointer}
.rz-child{padding-left:24px}
.rz-caret{width:16px;height:16px;padding:0;border:0;background:none;color:var(--muted);font-size:14px;line-height:16px;cursor:pointer;transition:transform .12s}.rz-caret.open{transform:rotate(90deg)}span.rz-caret{cursor:default}
.rz-main{display:flex;align-items:baseline;gap:10px;min-width:0;overflow:hidden;white-space:nowrap}
.rz-main strong{font-size:13px;font-weight:600;flex-shrink:0}
.rz-main.wrap{white-space:normal;padding:6px 0}.rz-main.wrap .rz-desc{overflow:visible;text-overflow:clip;line-height:1.5}.rz-desc{overflow:hidden;text-overflow:ellipsis;color:var(--muted);font-size:12.5px}
.rz-meta{display:flex;align-items:center;gap:10px;font-size:12px;color:var(--muted)}
.rz-count{font-variant-numeric:tabular-nums}.rz-cost{min-width:56px;text-align:right;font-variant-numeric:tabular-nums}
.rz-chip{max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;padding:1px 8px;border:1px solid var(--border);border-radius:999px;background:none;color:var(--text-secondary);font:inherit;font-size:11.5px;cursor:pointer}.rz-chip.off{color:var(--muted);text-decoration:line-through}
.rz-detail{padding:6px 4px 8px 50px;color:var(--text-secondary);font-size:12px;white-space:pre-wrap;border-bottom:1px solid color-mix(in srgb,var(--border) 60%,transparent)}
.rz-foot{margin-top:2px}
.link-button{background:none;border:0;padding:0;color:var(--accent);font:inherit;font-size:12px;cursor:pointer;text-decoration:underline}.link-button:disabled{opacity:.5;cursor:default}
.error-text{color:var(--danger)}
@media(max-width:600px){.rz-desc{display:none}.rz-toolbar .rz-expand{margin-left:0}}
</style>
