<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <p>查看机器人已加入的全部群，并按群配置回复时间、屏蔽账号、专属人设与插件开关</p>
      </div>
      <div class="group-manual-add">
        <input
          v-model="newGroupID"
          class="input"
          inputmode="numeric"
          placeholder="群号"
          @keydown.enter="addGroup"
        />
        <button class="btn primary" type="button" :disabled="newGroupID.trim().length === 0" @click="addGroup">
          <Plus :size="15" aria-hidden="true" />
          添加群配置
        </button>
      </div>
    </header>

    <div :aria-busy="!loaded || undefined">
      <div class="group-list-toolbar">
        <div class="group-list-summary">
          <SkeletonBlock v-if="!loaded" width="210px" height="27px" />
          <template v-else>
          <strong>{{ liveAvailable ? joinedCount : groups.length }}</strong>
          <span>{{ liveAvailable ? "个已加入群" : "个已保存配置" }}</span>
          <span class="muted">· {{ configuredCount }} 个独立配置</span>
          </template>
        </div>
        <div class="group-list-actions">
          <label class="group-search">
            <Search :size="15" aria-hidden="true" />
            <input v-model="searchQuery" type="search" placeholder="搜索群名或群号" aria-label="搜索群名或群号" />
          </label>
          <button class="btn" type="button" :disabled="refreshing" title="从机器人同步最新群列表" @click="load(true)">
            <RefreshCw :size="14" :class="{ spin: refreshing }" aria-hidden="true" />
            {{ refreshing ? "同步中…" : "刷新群列表" }}
          </button>
        </div>
      </div>

      <div v-if="loaded && botScope" class="group-scope-bar">
        <div class="group-scope-default">
          <label class="switch" :title="newGroupEnabled ? '新加入的群默认工作' : '新加入的群默认不工作'">
            <input type="checkbox" :checked="newGroupEnabled" :disabled="bulkBusy" @change="setNewGroupDefault($event)" />
            <span class="track" aria-hidden="true"></span>
          </label>
          <span class="group-scope-copy">
            <strong>{{ newGroupEnabled ? "新加入的群默认工作" : "新加入的群默认不工作" }}</strong>
            <span>没有单独设过的群按这个来</span>
          </span>
        </div>
        <div class="group-scope-actions">
          <div class="group-scope-buttons">
            <button class="btn" type="button" :disabled="bulkBusy || !filteredGroups.length" @click="setAllGroups(true)">全部启用</button>
            <button class="btn" type="button" :disabled="bulkBusy || !filteredGroups.length" @click="setAllGroups(false)">全部停用</button>
          </div>
          <span>只改下面列出的 {{ filteredGroups.length }} 个群，新加入的群不受影响</span>
        </div>
      </div>

      <div v-if="loaded && botScope && supportsGroupLevel" class="group-scope-bar group-scope-levels">
        <div class="group-scope-level">
          <label for="group-default-level">群等级门槛</label>
          <input
            id="group-default-level"
            class="input"
            inputmode="numeric"
            :value="defaultMinGroupLevel"
            :disabled="bulkBusy"
            @change="saveMinGroupLevel($event)"
          />
          <span>0 表示不限。指群内活跃度等级（Lv.1~6），不是账号等级。单个群可以在它的配置里覆盖。</span>
        </div>
        <div class="group-scope-level">
          <label for="group-default-unknown">等级读不到时</label>
          <AppSelect
            id="group-default-unknown"
            :model-value="defaultLevelUnknownPolicy"
            :options="levelUnknownOptions"
            :disabled="bulkBusy"
            @update:model-value="saveLevelUnknownPolicy($event as 'allow' | 'deny')"
          />
          <span>部分 OneBot 实现不提供群等级。选「拦截」会让这些实现下的群整体静音。</span>
        </div>
      </div>

      <div v-if="connectionPeers.length > 0" class="group-connection-note" role="status">
        <Share2 :size="16" aria-hidden="true" />
        <div class="group-connection-body">
          <p>
            这条连接上还有 {{ connectionPeers.length }} 台机器人。它们共用同一个平台账号：一条群消息会交给每一台，各自按自己的群开关决定回不回——同一个群有两台开着，群里看到的就是这个号连发几条。
          </p>
          <ul class="group-connection-list">
            <li v-for="peer in connectionPeers" :key="peer.bot_profile_id">
              <strong>{{ peer.name || peer.bot_profile_id }}</strong>
              <span v-if="!peer.enabled" class="muted">已停用，不参与回复</span>
              <template v-else>
                <span :class="{ 'connection-all-groups': peer.new_group_enabled }">
                  {{ peer.new_group_enabled ? "新群默认工作：没单独配过的群它都收" : "新群默认不工作" }}
                </span>
                <span v-if="peer.enabled_groups?.length" class="muted">
                  已开 {{ peer.enabled_groups.length }} 个群：{{ peer.enabled_groups.slice(0, 8).join("、")
                  }}<template v-if="peer.enabled_groups.length > 8">…</template>
                </span>
                <span v-else-if="!peer.new_group_enabled" class="muted">没有开着的群</span>
              </template>
            </li>
          </ul>
        </div>
      </div>

      <div v-if="syncWarning" class="group-sync-warning" role="status">
        <WifiOff :size="16" aria-hidden="true" />
        <span>{{ syncWarning }}</span>
      </div>

      <LoadingSkeleton v-if="!loaded" kind="groups" label="正在加载群列表" />
      <div v-else-if="filteredGroups.length > 0" class="group-grid">
        <section v-for="group in filteredGroups" :key="group.group_id" class="group-card">
          <div class="group-card-head">
            <div class="group-identity">
              <img v-if="group.avatar_url" :src="group.avatar_url" :alt="group.group_name || `群 ${group.group_id}`" @error="hideBrokenAvatar" />
              <div class="group-identity-copy">
                <h2>{{ group.group_name || `群 ${group.group_id}` }}</h2>
                <span class="mono">{{ group.group_id }}</span>
              </div>
            </div>
            <label class="switch" :title="group.enabled ? '在本群启用' : '在本群停用'">
              <input
                type="checkbox"
                :checked="group.enabled"
                :disabled="togglingGroupID === group.group_id"
                @change="toggleGroup(group, $event)"
              />
              <span class="track" aria-hidden="true"></span>
            </label>
          </div>
          <div class="group-card-badges">
            <span v-if="liveAvailable" class="badge" :class="{ accent: group.joined }">{{ group.joined ? "已加入" : "当前未加入" }}</span>
            <span class="badge" :class="{ accent: group.configured }">{{ group.configured ? "已配置" : "跟随全局" }}</span>
            <span v-if="group.member_count" class="badge">
              <Users :size="12" aria-hidden="true" />
              {{ group.member_count }}<template v-if="group.max_member_count"> / {{ group.max_member_count }}</template>
            </span>
            <span v-if="group.configured && group.persona_id" class="badge accent">人设：{{ personaNameOf(group.persona_id) }}</span>
            <span v-else-if="group.configured && group.system_prompt" class="badge">专属人设</span>
            <span v-if="group.configured && group.participation" class="badge accent">{{ participationSummary(group.participation) }}</span>
            <span v-if="group.configured && overrideCount(group) > 0" class="badge">插件覆盖 {{ overrideCount(group) }}</span>
            <span v-if="group.configured && group.welcome_enabled" class="badge">入群欢迎</span>
            <span v-if="group.configured && group.reply_gate?.active_hours_enabled" class="badge">
              回复 {{ group.reply_gate.active_start }}–{{ group.reply_gate.active_end }}
            </span>
            <span v-if="group.configured && group.recall_reply_auto_delete_enabled" class="badge">
              撤回回复保留 {{ group.recall_reply_auto_delete_delay_seconds ?? defaultRecallReplyAutoDeleteDelaySeconds }} 秒
            </span>
            <span v-if="group.configured && group.reply_account_safety_audit_enabled === false" class="badge">本群关闭安全审核</span>
            <span v-else-if="group.configured && group.reply_account_safety_audit_enabled === true" class="badge accent">本群开启安全审核</span>
            <span v-if="group.configured && blockedUserCount(group) > 0" class="badge">屏蔽 {{ blockedUserCount(group) }} 人</span>
            <span v-if="group.configured && hasOtherReplyGateRules(group)" class="badge">专属回复规则</span>
            <span v-if="group.shared_with?.length" class="badge warn" :title="sharedBotsTitle(group)">
              同连接 {{ group.shared_with.length + 1 }} 台都在回
            </span>
            <span v-if="quotaBadge(group)" class="badge" :class="quotaBadge(group)!.tone" :title="quotaBadge(group)!.title">
              {{ quotaBadge(group)!.text }}
            </span>
          </div>
          <p class="group-card-desc">
            {{ group.system_prompt ? truncate(group.system_prompt, 68) : group.configured ? "沿用全局人设与默认行为。" : "尚未设置群级覆盖，当前跟随全局配置。" }}
          </p>
          <div class="group-card-foot">
            <span class="muted">{{ group.enabled ? "机器人已启用" : "机器人已停用" }}</span>
            <div class="cluster" style="gap: 8px">
              <button v-if="group.configured" class="btn small ghost" type="button" title="删除群配置" aria-label="删除群配置" :disabled="deleting" @click="pendingDelete = group">
                <Trash2 :size="14" aria-hidden="true" />
              </button>
              <button class="btn small ghost" type="button" @click="openRelations(group, group.group_name)">
                <Share2 :size="13" aria-hidden="true" />
                关系图
              </button>
              <button class="btn small" type="button" @click="openEditor(group, group.group_name)">
                <SlidersHorizontal :size="13" aria-hidden="true" />
                配置
              </button>
            </div>
          </div>
        </section>
      </div>
      <EmptyState v-else-if="groups.length > 0" title="没有匹配的群" hint="换一个群名或群号搜索。" />
      <EmptyState
        v-else-if="syncWarning"
        title="暂时无法读取群列表"
        hint="机器人连接后点击刷新群列表；也可以先输入群号创建配置。"
      />
      <EmptyState v-else title="机器人还没有加入群" hint="机器人加入群后会自动显示在这里，也可以输入群号预先创建配置。" />
    </div>

    <Modal v-if="pendingDelete" title="删除群配置" @close="!deleting && (pendingDelete = null)">
      <p>删除 {{ pendingDelete.group_name || pendingDelete.group_id }} 的独立配置？删除后恢复全局规则（可能重新启用回复），不会退出群聊或删除聊天记录。已加入的群仍会显示。</p>
      <template #footer>
        <button class="btn ghost" :disabled="deleting" @click="pendingDelete = null">取消</button>
        <button class="btn" :disabled="deleting" @click="removeGroup"><Trash2 :size="15" />{{ deleting ? "删除中…" : "删除配置" }}</button>
      </template>
    </Modal>

    <!-- 群配置编辑弹窗 -->
    <Modal
      v-if="relationsGroupID"
      :title="`${relationsGroupName || `群 ${relationsGroupID}`} · 关系图`"
      wide
      @close="closeRelations"
    >
      <div class="stack" style="gap: 12px">
        <div class="segmented" role="radiogroup" aria-label="按时间范围统计关系">
          <button
            v-for="option in relationRangeOptions"
            :key="option.value"
            type="button"
            role="radio"
            :aria-checked="relationsRange === option.value"
            :class="{ active: relationsRange === option.value }"
            @click="selectRelationsRange(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
        <p v-if="relationsLoading" class="muted">正在统计…</p>
        <GroupRelationChart v-else-if="relationsGraph" :graph="relationsGraph" />
      </div>
    </Modal>

    <Modal v-if="editing" :title="`${editingGroupName || `群 ${editing.group_id}`} · 配置`" wide @close="editing = null">
      <div class="form-grid">
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">在本群启用机器人</span>
          </label>
        </div>
        <div class="field wide">
          <label for="group-triggers">本群触发词（逗号分隔，留空用全局）</label>
          <input id="group-triggers" v-model="triggersDraft" class="input" placeholder="Diana,diana" />
        </div>
        <div class="field wide">
          <label for="group-trigger-mode">本群触发词匹配（留空用全局）</label>
          <AppSelect
            id="group-trigger-mode"
            :model-value="editing.group_trigger_mode ?? ''"
            :options="groupTriggerModeOptions"
            @update:model-value="(value) => { if (editing) editing.group_trigger_mode = value as typeof editing.group_trigger_mode; }"
          />
          <span class="hint">智能档下，群里谈论机器人而不是叫它的消息不会强制回复。</span>
        </div>
        <div class="field wide">
          <label for="group-persona-source">本群人设</label>
          <AppSelect
            id="group-persona-source"
            :model-value="personaSource"
            :options="personaSourceOptions"
            @update:model-value="(value) => setPersonaSource(String(value))"
          />
          <textarea
            v-if="personaSource !== ''"
            id="group-prompt"
            v-model="editing.system_prompt"
            class="textarea"
            rows="3"
            :placeholder="personaPlaceholder"
            @input="detachEditedPersona"
          ></textarea>
          <span v-if="editing.persona_id" class="hint">
            内容来自人设库「{{ linkedPersonaName }}」，连同表达风格、自称和句尾语气词一起生效，人设库里修改后本群自动更新。在这里改了就变成本群自定义。
          </span>
          <template v-else-if="personaSource === 'custom'">
            <span class="hint">只用这里的文字，改机器人人设或人设库都不影响本群。</span>
            <div v-if="editedGroupLibraryPersona" class="cluster">
              <button class="btn small" type="button" :disabled="personaWriteBackBusy" @click="writeBackGroupPersona">
                更新人设库「{{ editedGroupLibraryPersona.name }}」
              </button>
              <span class="hint">写回后本群重新绑定它，绑定这一套的机器人和群一起更新。</span>
            </div>
          </template>
          <span v-else class="hint">本群一直跟着{{ inheritedPersonaOwner }}走，改机器人人设时本群也跟着变。</span>
          <details v-if="inheritedPersona && !editing.persona_id" class="inherited-persona">
            <summary>{{ inheritedPersonaOwner }}当前的人设</summary>
            <p>{{ inheritedPersona }}</p>
          </details>
        </div>
        <div class="field wide">
          <label>接话设置</label>
          <ParticipationControls :key="`${editing.bot_profile_id}:${editing.group_id}`" :model-value="editing.participation" :level="groupReplyDesireValue(editing)" :inherited-value="participationDefaults[editing.bot_profile_id || botScope || '']" :criteria="editing.proactive_reply_extra_criteria" inheritable @update:model-value="setGroupParticipation" @update:criteria="value => { if (editing) editing.proactive_reply_extra_criteria = value; }" />
        </div>
        <div class="field wide">
          <label>本群补充标记的机器人</label>
          <BotMarkerList :key="`${editing.bot_profile_id}:${editing.group_id}`" v-model="editing.marked_bot_ids" :inherited-ids="markedBotDefaults[editing.bot_profile_id || botScope || '']" />
        </div>
        <div class="field">
          <label for="group-action-description">动作描写</label>
          <AppSelect
            id="group-action-description"
            :model-value="editing.action_description_enabled === undefined ? '' : editing.action_description_enabled ? 'on' : 'off'"
            :options="groupActionDescriptionOptions"
            @update:model-value="(value) => { if (editing) { detachEditedPersona(); editing.action_description_enabled = value === '' ? undefined : value === 'on'; } }"
          />
          <span v-if="editing.persona_id" class="hint">跟着人设库「{{ linkedPersonaName }}」走，改了就变成本群自定义。</span>
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.welcome_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">开启入群欢迎</span>
          </label>
        </div>
        <div v-if="editing.welcome_enabled" class="field wide">
          <label for="group-welcome">欢迎语</label>
          <textarea id="group-welcome" v-model="editing.welcome_message" class="textarea" rows="2"></textarea>
        </div>
        <div v-if="editing.welcome_enabled" class="field">
          <label for="group-welcome-mode">欢迎词模式</label>
          <AppSelect
            id="group-welcome-mode"
            :model-value="editing.welcome_mode ?? ''"
            :options="groupWelcomeModeOptions"
            @update:model-value="(value) => { if (editing) editing.welcome_mode = (value || undefined) as BotGroupConfig['welcome_mode']; }"
          />
        </div>
        <div v-if="editing.welcome_enabled && (editing.welcome_mode ?? '') !== 'fixed'" class="field wide">
          <label for="group-welcome-templates">欢迎词模板池</label>
          <textarea
            id="group-welcome-templates"
            v-model="welcomeTemplatesDraft"
            class="textarea"
            rows="3"
            placeholder="每行一条候选，发送时随机抽一条；{user_id} 会替换成新成员 ID。留空跟随机器人。LLM 模式冷却或失败时也从这里回落。"
          ></textarea>
        </div>
        <div v-if="editing.welcome_enabled && editing.welcome_mode === 'llm'" class="field">
          <label for="group-welcome-cooldown">LLM 欢迎冷却（秒/群）</label>
          <input
            id="group-welcome-cooldown"
            v-model.number="editing.welcome_llm_cooldown_seconds"
            class="input"
            inputmode="numeric"
            placeholder="留空跟随机器人"
          />
          <span class="hint">冷却期内新成员入群改发模板池/固定文本，避免进出群刷屏消耗 Token。</span>
        </div>
        <div class="field">
          <label for="group-call-quota">模型额度 · 5 小时调用次数</label>
          <input id="group-call-quota" v-model.number="editing.model_call_quota" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">这个群名下的每次模型调用都算，含路由判断和工具步。留空跟随机器人那一档。</span>
        </div>
        <div class="field">
          <label for="group-sample">回复抽样率（%）</label>
          <input id="group-sample" v-model.number="editing.reply_sample_percent" class="input" type="number" min="0" max="100" step="1" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">没 @、没引用、没叫名字的消息，只有这个比例交给模型判断要不要接话，没抽中的一次调用都不花。被点名的照常回复。</span>
        </div>
        <div v-if="editingQuota" class="field wide quota-usage">
          <div class="cluster" style="justify-content: space-between; gap: 8px">
            <span class="muted">{{ quotaWindowLabel }}已用</span>
            <span>{{ editingQuota.text }}</span>
          </div>
          <div class="quota-track" role="img" :aria-label="`${editingQuota.text}，用满 ${editingQuota.percent}%`">
            <span :class="{ full: editingQuota.percent >= 100 }" :style="{ width: `${Math.min(100, editingQuota.percent)}%` }"></span>
          </div>
          <span class="hint">{{ editingQuota.detail }}</span>
        </div>
        <p class="hint field wide">
          两项留空都跟随机器人配置，两边都没填就是不限额、不抽样。额度用满之后这个群暂停一切花 token 的环节，消息照常进历史和长期记忆，
          窗口滚过去自动恢复，不需要手动解除。主人不受额度和抽样限制。
        </p>
        <div class="field">
          <label for="group-history-budget">回复历史 token 预算</label>
          <input id="group-history-budget" v-model.number="editing.recent_history_token_budget" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
        </div>
        <div class="field">
          <label for="group-context">历史查询条数上限</label>
          <input id="group-context" v-model.number="editing.recent_context_limit" class="input" inputmode="numeric" />
        </div>
        <div class="field">
          <label for="group-maxcontext">单次请求上下文上限</label>
          <input id="group-maxcontext" v-model.number="editing.max_context_tokens" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
        </div>
        <div class="field">
          <label for="group-maxreply">单条回复上限（字符）</label>
          <input id="group-maxreply" v-model.number="editing.max_reply_chars" class="input" inputmode="numeric" />
        </div>
        <div class="field wide">
          <label for="group-muted-pause">本群被禁言时暂停回复</label>
          <AppSelect
            id="group-muted-pause"
            :model-value="editing.muted_reply_pause_enabled == null ? '' : editing.muted_reply_pause_enabled ? 'on' : 'off'"
            :options="groupMutedReplyPauseOptions"
            @update:model-value="(value) => { if (editing) editing.muted_reply_pause_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">暂停期间消息只记入上下文，不生成回复；解禁后从新消息开始回复。</span>
        </div>
        <div v-for="item in mutedStepItems" :key="item.key" class="field">
          <label :for="`group-${item.key}`">{{ item.label }}</label>
          <AppSelect
            :id="`group-${item.key}`"
            :model-value="editing[item.key] == null ? '' : editing[item.key] ? 'on' : 'off'"
            :options="followBotOptions(mutedStepDefaults[item.key], item.fallback)"
            @update:model-value="(value) => { if (editing) editing[item.key] = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">{{ item.hint }}</span>
        </div>
        <div class="field wide">
          <label for="group-natural-split">本群允许多条发送</label>
          <AppSelect
            id="group-natural-split"
            :model-value="editing.natural_reply_split_enabled == null ? '' : editing.natural_reply_split_enabled ? 'on' : 'off'"
            :options="groupNaturalReplySplitOptions"
            @update:model-value="(value) => { if (editing) editing.natural_reply_split_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">换行不分条（开启换行分条除外）；开启后只认显式分条标记，关闭后单条发送、超限压缩。本轮用户明确要求优先。</span>
        </div>
        <div class="field wide">
          <label for="group-line-split">本群换行分条发送</label>
          <AppSelect
            id="group-line-split"
            :model-value="editing.reply_line_split_enabled == null ? '' : editing.reply_line_split_enabled ? 'on' : 'off'"
            :options="groupLineSplitOptions"
            @update:model-value="(value) => { if (editing) editing.reply_line_split_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">每换一行另发一条；列表、表格和代码块整块发。本群不允许多条发送时不生效。</span>
        </div>
        <div class="field wide">
          <label for="group-typing-delay">本群模拟打字延时</label>
          <AppSelect
            id="group-typing-delay"
            :model-value="editing.typing_delay_enabled == null ? '' : editing.typing_delay_enabled ? 'on' : 'off'"
            :options="groupTypingDelayOptions"
            @update:model-value="(value) => { if (editing) editing.typing_delay_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">连发时按下一条的字数停顿；打字速度跟随机器人设置。</span>
        </div>
        <div class="field wide">
          <label for="group-preserve-lines">本群普通段落换行</label>
          <AppSelect
            id="group-preserve-lines"
            :model-value="editing.reply_preserve_line_breaks == null ? '' : editing.reply_preserve_line_breaks ? 'on' : 'off'"
            :options="[{ value: '', label: '跟随机器人' }, { value: 'on', label: '保留换行' }, { value: 'off', label: '收拢普通段落' }]"
            @update:model-value="(value) => { if (editing) editing.reply_preserve_line_breaks = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">仅影响消息内部排版，不改变发送条数；列表、代码、表格保留结构，本轮排版要求优先。</span>
        </div>
        <div class="field">
          <label for="group-reply-merge-confidence">合并回复置信度阈值（%）</label>
          <input id="group-reply-merge-confidence" v-model.number="editing.reply_merge_confidence_percent" class="input" type="number" min="1" max="100" step="1" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">只影响同一用户连续消息的合并，不改变主动接话档位。可填 1–100，越低越容易合并；留空跟随机器人当前设置。</span>
        </div>
        <div class="field wide">
          <label for="group-account-safety">本群账号安全审核</label>
          <AppSelect
            id="group-account-safety"
            :model-value="editing.reply_account_safety_audit_enabled == null ? '' : editing.reply_account_safety_audit_enabled ? 'on' : 'off'"
            :options="groupAccountSafetyOptions"
            @update:model-value="(value) => { if (editing) editing.reply_account_safety_audit_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">关闭后，本群主动插话和直接回复都不做账号安全审核；准确度审核和防机器人循环不受影响。</span>
        </div>
        <div class="field wide">
          <label for="group-account-safety-prompt">本群账号安全审核规则（留空跟随机器人）</label>
          <textarea
            id="group-account-safety-prompt"
            v-model="editing.reply_account_safety_audit_prompt"
            class="textarea"
            rows="5"
            maxlength="8000"
            placeholder="填写本群需要拦截的内容范围"
          ></textarea>
          <span class="hint">填写后替代机器人级或内置风险范围，仅用于本群。</span>
        </div>
        <div v-if="supportsGroupLevel" class="field">
          <label for="group-forward-len">合并转发字数</label>
          <input id="group-forward-len" v-model.number="editing.forward_reply_threshold" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="无上限" />
          <span class="hint">允许多条发送时，整轮正文超过此值触发卡片；0 或留空关闭此条件。仅 OneBot 支持。</span>
        </div>
        <div v-if="supportsGroupLevel" class="field">
          <label for="group-forward-chunks">合并转发块数</label>
          <input id="group-forward-chunks" v-model.number="editing.forward_reply_chunk_threshold" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="无上限" />
          <span class="hint">实际消息数超过此值触发卡片，填 4 表示至少 5 条；0 或留空关闭此条件。不按正文行数计数。</span>
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.recall_reply_auto_delete_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">本群查看撤回消息后自动撤回回复</span>
          </label>
          <span class="hint">关闭时，查看撤回记录产生的回复会一直保留。</span>
        </div>
        <div v-if="editing.recall_reply_auto_delete_enabled" class="field">
          <label for="group-recall-delete-delay">回复保留时间（秒）</label>
          <input
            id="group-recall-delete-delay"
            v-model.number="editing.recall_reply_auto_delete_delay_seconds"
            class="input"
            type="number"
            min="1"
            :max="maximumRecallReplyAutoDeleteDelaySeconds"
            step="1"
            inputmode="numeric"
          />
        </div>
        <div v-for="field in sendRetryFields" :key="field.key" class="field">
          <label :for="`group-${field.key}`">{{ field.label }}</label>
          <input
            :id="`group-${field.key}`"
            v-model.number="editing[field.key]"
            class="input"
            type="number"
            :min="field.min"
            :max="field.max"
            step="1"
            inputmode="numeric"
            :placeholder="`跟随机器人（${botSendRetryValue(field)}）`"
          />
          <span class="hint">{{ field.hint }}</span>
        </div>
        <div class="field wide">
          <label>本群回复时间与屏蔽账号</label>
          <ReplyGateForm v-model="editing.reply_gate" allow-inherit id-prefix="group-gate" :supports-group-level="supportsGroupLevel" />
        </div>
        <div v-if="extensions.length" class="field wide">
          <label>本群扩展</label>
          <p class="hint">按群覆盖 MCP 与 Skill：档位不设就跟随机器人，白名单里的人不看档位也能用，黑名单一律不给。判定顺序是停用 &gt; 黑名单 &gt; 白名单 &gt; 档位，「停用」对所有人生效，主人也一样。机器人那一档是默认值——扩展页关掉的服务，在这里给本群选一个档位就能单独用起来；只有扩展页里的全局「服务可用」关掉时，本群怎么选都没用。</p>
          <div class="row-list" style="margin-top: 6px">
            <div v-for="item in extensions" :key="item.id" class="row-item group-plugin-row">
              <div class="group-plugin-row-head">
                <div class="row-main">
                  <div class="row-title">{{ item.name }}<span class="badge">{{ item.kind === 'skill' ? 'Skill' : 'MCP' }}</span></div>
                  <!-- 全局停用和「这台机器人默认不开」是两回事：前者本群怎么选都没用，
                       说清楚，别让人对着一排点不动的按钮猜。 -->
                  <div class="row-sub">{{ item.available === false ? '全局停用，本群改不动' : `机器人：${extensionTierLabel(botTierOf(item))}` }}</div>
                </div>
                <div class="segmented">
                  <button type="button" :disabled="item.available === false" :class="{ active: !tierOf(item.id) }" @click="setTier(item.id, undefined)">跟随</button>
                  <button v-for="tier in extensionTiers" :key="tier.value" type="button" :disabled="item.available === false" :class="{ active: tierOf(item.id) === tier.value }" :title="tier.hint" @click="setTier(item.id, tier.value)">{{ tier.label }}</button>
                </div>
              </div>
              <!-- 「跟随」是本群完全不干预，连名单也不该有；停用时两份名单同样没有意义。 -->
              <div v-if="tierOf(item.id) && tierOf(item.id) !== 'off'" class="group-extension-lists">
                <div class="field">
                  <label :for="`group-extension-allow-${item.id}`">本群白名单</label>
                  <IdChipInput
                    :input-id="`group-extension-allow-${item.id}`"
                    :model-value="listOf(item.id, 'allow')"
                    placeholder="填账号后回车，这些人不看档位也能用"
                    :resolve-names="resolveAccountNames"
                    @update:model-value="setList(item.id, 'allow', $event)"
                  />
                </div>
                <div class="field">
                  <label :for="`group-extension-deny-${item.id}`">本群黑名单</label>
                  <IdChipInput
                    :input-id="`group-extension-deny-${item.id}`"
                    :model-value="listOf(item.id, 'deny')"
                    placeholder="填账号后回车，这些人一律不给用"
                    :resolve-names="resolveAccountNames"
                    @update:model-value="setList(item.id, 'deny', $event)"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
        <div class="field wide">
          <label>本群插件</label>
          <div class="row-list" style="margin-top: 6px">
            <div v-for="plugin in plugins" :key="plugin.manifest.id" class="row-item group-plugin-row">
              <div class="group-plugin-row-head">
                <div class="row-main">
                  <div class="row-title">{{ plugin.manifest.name }}</div>
                  <div class="row-sub">全局：{{ plugin.enabled ? "已启用" : "已停用" }}</div>
                </div>
                <div class="segmented">
                  <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === undefined }" @click="setOverride(plugin.manifest.id, undefined)">
                    跟随
                  </button>
                  <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === true }" @click="setOverride(plugin.manifest.id, true)">
                    开
                  </button>
                  <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === false }" @click="setOverride(plugin.manifest.id, false)">
                    关
                  </button>
                </div>
              </div>
              <GroupPluginSettings
                v-if="plugin.installed && plugin.manifest.settings?.length"
                :plugin="plugin"
                :model-value="settingOverridesOf(plugin.manifest.id)"
                @update:model-value="setPluginSettingOverrides(plugin.manifest.id, $event)"
              />
            </div>
          </div>
        </div>
      </div>
      <template #footer>
        <button class="btn ghost" type="button" @click="editing = null">取消</button>
        <button class="btn primary" type="button" :disabled="saving" @click="saveEditing">
          <Save :size="15" aria-hidden="true" />
          {{ saving ? "保存中…" : "保存" }}
        </button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { useConfigurationRefresh } from "../configuration-sync";
import { computed, onMounted, ref, watch } from "vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import { askConfirm } from "../confirm";
import { botScope } from "../bot-scope";
import { Plus, RefreshCw, Save, Search, Share2, SlidersHorizontal, Trash2, Users, WifiOff } from "@lucide/vue";
import {
  type ConnectionPeer,
  getBotProfileConfig,
  getBotPlatforms,
  listBotGroups,
  saveBotGroup,
  saveBotGroupSwitches,
  deleteBotGroup,
  getGroupRelations,
  fetchAssistantUserNames,
  listManagedExtensions,
  listPersonas,
  savePersona,
  type ManagedExtension,
  type Persona,
  type PluginState,
  type BotGroupConfig,
  type BotGroupSummary,
  type AssistantEventRange,
  type GroupRelationGraph
} from "../api";
import EmptyState from "../components/EmptyState.vue";
import IdChipInput from "../components/IdChipInput.vue";
import GroupRelationChart from "../components/GroupRelationChart.vue";
import GroupPluginSettings from "../components/GroupPluginSettings.vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import ParticipationControls from "../components/ParticipationControls.vue";
import BotMarkerList from "../components/BotMarkerList.vue";
import { participationFromConfig, participationLevelLabel, participationPresetName, type ParticipationPreferences } from "../participation";
import Modal from "../components/Modal.vue";
import ReplyGateForm from "../components/ReplyGateForm.vue";
import { sendRetryFields, sendRetryPayload, sendRetryValidationError, withUnsetSendRetryCleared, type SendRetryField, type SendRetrySettings } from "../send-retry-settings";

// 空值代表「跟随全局」，与后端把空字符串当成未覆盖的约定一致。
const groupTriggerModeOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "smart", label: "智能" },
  { value: "strict", label: "严格" },
  { value: "loose", label: "宽松" }
];


const groupActionDescriptionOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "on", label: "开启" },
  { value: "off", label: "关闭" }
];

const groupWelcomeModeOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "fixed", label: "固定文本" },
  { value: "template", label: "口吻模板池" },
  { value: "llm", label: "按人设实时生成" }
];
import { toastError, toastSuccess } from "../toast";

const groups = ref<BotGroupSummary[]>([]);
const pendingDelete = ref<BotGroupSummary | null>(null);
const deleting = ref(false);

async function removeGroup(): Promise<void> {
  const group = pendingDelete.value;
  if (!group || deleting.value) return;
  deleting.value = true;
  try {
    await deleteBotGroup(group.group_id, group.bot_profile_id ?? "");
    pendingDelete.value = null;
    toastSuccess("群配置已删除，恢复全局规则");
    await load();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally { deleting.value = false; }
}
// 群等级只有 OneBot v11 有；按当前激活的机器人平台决定要不要显示这一项。
const supportsGroupLevel = ref(true);
const plugins = ref<PluginState[]>([]);
const extensions = ref<ManagedExtension[]>([]);
const extensionTiers = [
  { value: "off", label: "停用", hint: "本群谁都不用它，主人也一样" },
  { value: "owner", label: "仅主人", hint: "本群只有主人能用" },
  { value: "admins", label: "群管", hint: "本群群主和管理员也能用" },
  { value: "members", label: "群成员", hint: "本群成员都能用" }
] as const;
const loaded = ref(false);
const refreshing = ref(false);
const liveAvailable = ref(false);
const syncWarning = ref("");
// 复用同一条连接的其它机器人：路由表散在各台自己的配置里，这份是那张全貌。
const connectionPeers = ref<ConnectionPeer[]>([]);
const searchQuery = ref("");
const newGroupID = ref("");
const relationRangeOptions: Array<{ value: AssistantEventRange; label: string }> = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
  { value: "all", label: "全部" }
];
const relationsGroupID = ref("");
const relationsGroupName = ref("");
const relationsRange = ref<AssistantEventRange>("7d");
const relationsGraph = ref<GroupRelationGraph | null>(null);
const relationsLoading = ref(false);

function openRelations(group: BotGroupConfig, groupName = ""): void {
  relationsGroupID.value = group.group_id;
  relationsGroupName.value = groupName;
  relationsGraph.value = null;
  void loadRelations();
}

function closeRelations(): void {
  relationsGroupID.value = "";
  relationsGraph.value = null;
}

function selectRelationsRange(value: AssistantEventRange): void {
  if (relationsRange.value === value) return;
  relationsRange.value = value;
  relationsGraph.value = null;
  void loadRelations();
}

async function loadRelations(): Promise<void> {
  const groupID = relationsGroupID.value;
  if (!groupID) return;
  relationsLoading.value = true;
  try {
    const response = await getGroupRelations(groupID, relationsRange.value);
    // 期间可能已经关掉弹窗或换了群，回来的结果不该覆盖当前状态。
    if (relationsGroupID.value !== groupID) return;
    relationsGraph.value = response.graph;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "关系图加载失败");
  } finally {
    relationsLoading.value = false;
  }
}

const editing = ref<BotGroupConfig | null>(null);

const quotaWindowSeconds = ref(0);

// 弹窗里这一条是「我刚填的这个数，现在用掉多少了」。
const editingQuota = computed(() => {
  const groupID = editing.value?.group_id;
  if (!groupID) return undefined;
  const summary = groups.value.find((group) => group.group_id === groupID);
  if (!summary) return undefined;
  const callLimit = summary.quota_call_limit ?? 0;
  if (callLimit <= 0) return undefined;
  const callsUsed = summary.quota_calls_used ?? 0;
  const percent = Math.round((callsUsed / callLimit) * 100);
  const detail =
    percent >= 100
      ? "已用满，这个群暂停一切花 token 的环节；消息照常进历史和长期记忆，窗口滚过去自动恢复。"
      : `剩 ${Math.max(0, callLimit - callsUsed)} 次调用。窗口是滚动的，不在整点清零。`;
  return { text: `调用 ${callsUsed} / ${callLimit} 次`, detail, percent };
});


const quotaWindowLabel = computed(() => {
  const hours = quotaWindowSeconds.value / 3600;
  if (hours <= 0) return "额度窗口";
  return Number.isInteger(hours) ? `最近 ${hours} 小时` : `最近 ${hours.toFixed(1)} 小时`;
});

// 额度是个「悄悄生效」的闸门：用满之后机器人就是不说话，不摆出进度来没人知道
// 是撞了额度还是坏了。快满和已满分开着色。
function quotaBadge(group: BotGroupSummary): { text: string; title: string; tone: string } | undefined {
  const callLimit = group.quota_call_limit ?? 0;
  if (callLimit <= 0) return undefined;
  const callsUsed = group.quota_calls_used ?? 0;
  const ratio = callsUsed / callLimit;
  const tone = ratio >= 1 ? "warn" : ratio >= 0.8 ? "accent" : "";
  const suffix = ratio >= 1 ? "，已暂停一切花 token 的环节，窗口滚过去自动恢复" : "";
  return { text: `额度 ${callsUsed}/${callLimit} 次`, title: `${quotaWindowLabel.value}：调用 ${callsUsed}/${callLimit} 次${suffix}`, tone };
}
const editingGroupName = ref("");
const triggersDraft = ref("");
const welcomeTemplatesDraft = ref("");
const saving = ref(false);
const togglingGroupID = ref("");
const defaultRecallReplyAutoDeleteEnabled = ref(false);
// 自然分条默认是开的，跟机器人配置那边的缺省一致。
const naturalReplySplitDefaults = ref<Record<string, boolean>>({});
const defaultNaturalReplySplitEnabled = computed(() =>
  naturalReplySplitDefaults.value[editing.value?.bot_profile_id || botScope.value]
    ?? naturalReplySplitDefaults.value[""]
    ?? true
);
// 被禁言时暂停回复，以及暂停期间哪些环节照常执行。缺省值跟机器人配置那边一致。
type MutedStepKey = "muted_image_description_enabled" | "muted_voice_transcription_enabled" | "muted_reply_judgment_enabled";
const mutedStepItems: { key: MutedStepKey; label: string; fallback: boolean; hint: string }[] = [
  { key: "muted_image_description_enabled", label: "暂停期间识别图片", fallback: true, hint: "关掉能省下识图费用，但这段历史里的图片没有文字描述。" },
  { key: "muted_voice_transcription_enabled", label: "暂停期间语音转文字", fallback: true, hint: "关掉能省下转写费用，但这段历史里的语音没有文字。" },
  { key: "muted_reply_judgment_enabled", label: "暂停期间回复判断", fallback: false, hint: "开启后照常判断，该回的记为「判断该回，但禁言中未发送」，不生成也不发送。" }
];
const mutedReplyPauseDefaults = ref<Record<string, boolean>>({});
const mutedStepDefaults = ref<Record<MutedStepKey, Record<string, boolean>>>({
  muted_image_description_enabled: {},
  muted_voice_transcription_enabled: {},
  muted_reply_judgment_enabled: {}
});
function followBotOptions(defaults: Record<string, boolean>, fallback = true): AppSelectOption[] {
  const botDefault = defaults[editing.value?.bot_profile_id || botScope.value] ?? defaults[""] ?? fallback;
  return [
    { value: "", label: `跟随机器人（${botDefault ? "开启" : "关闭"}）` },
    { value: "on", label: "开启" },
    { value: "off", label: "关闭" }
  ];
}
const groupMutedReplyPauseOptions = computed(() => followBotOptions(mutedReplyPauseDefaults.value));
// 分群的重发参数留空跟随机器人，占位符显示机器人当前生效的值。
const sendRetryDefaults = ref<Record<string, SendRetrySettings>>({});
function botSendRetryValue(field: SendRetryField): number {
  const bot = sendRetryDefaults.value[editing.value?.bot_profile_id || botScope.value] ?? sendRetryDefaults.value[""];
  const value = Number(bot?.[field.key]);
  return Number.isInteger(value) && value > 0 ? value : field.fallback;
}
const groupNaturalReplySplitOptions = computed<AppSelectOption[]>(() => [
  { value: "", label: `跟随机器人（${defaultNaturalReplySplitEnabled.value ? "开启" : "关闭"}）` },
  { value: "on", label: "开启" },
  { value: "off", label: "关闭" }
]);
// 换行分条和模拟打字默认关闭，同样按所属机器人显示继承值。
const lineSplitDefaults = ref<Record<string, boolean>>({});
const typingDelayDefaults = ref<Record<string, boolean>>({});
function inheritedSwitchOptions(defaults: Record<string, boolean>): AppSelectOption[] {
  const inherited = defaults[editing.value?.bot_profile_id || botScope.value] ?? defaults[""] ?? false;
  return [
    { value: "", label: `跟随机器人（${inherited ? "开启" : "关闭"}）` },
    { value: "on", label: "开启" },
    { value: "off", label: "关闭" }
  ];
}
const groupLineSplitOptions = computed(() => inheritedSwitchOptions(lineSplitDefaults.value));
const groupTypingDelayOptions = computed(() => inheritedSwitchOptions(typingDelayDefaults.value));
const groupAccountSafetyOptions: AppSelectOption[] = [
  { value: "", label: "跟随机器人" },
  { value: "on", label: "开启（主动和直接回复）" },
  { value: "off", label: "关闭（主动和直接回复）" }
];
const defaultSocialReplyEnabled = ref(false);
const participationDefaults = ref<Record<string, ParticipationPreferences>>({});
const markedBotDefaults = ref<Record<string,string[]>>({});
// 群人设默认跟随所属机器人，编辑框留空时得让人看见继承的是谁的哪段文字。
const personaDefaults = ref<Record<string, { name: string; prompt: string }>>({});
const inheritedPersonaProfile = computed(() =>
  personaDefaults.value[editing.value?.bot_profile_id || botScope.value] ?? personaDefaults.value[""]
);
const inheritedPersona = computed(() => inheritedPersonaProfile.value?.prompt ?? "");
const inheritedPersonaOwner = computed(() => {
  const name = inheritedPersonaProfile.value?.name?.trim();
  return name ? `「${name}」` : "所属机器人";
});
// 人设库：群人设可以绑定其中一套，库里改了由后端同步写进群配置。
const personaLibrary = ref<Persona[]>([]);
function personaNameOf(id?: string): string {
  return personaLibrary.value.find((persona) => persona.id === id)?.name ?? "已删除的人设";
}
const linkedPersonaName = computed(() => personaNameOf(editing.value?.persona_id));
// 来源三选一：留空跟随机器人、本群自定义、绑定人设库里的一套。
const personaSource = computed(() => {
  const current = editing.value;
  if (!current) return "";
  if (current.persona_id) return current.persona_id;
  return current.system_prompt?.trim() ? "custom" : "";
});
const personaSourceOptions = computed<AppSelectOption[]>(() => {
  const options: AppSelectOption[] = [
    { value: "", label: `跟随${inheritedPersonaOwner.value}` },
    { value: "custom", label: "本群自定义" },
    ...personaLibrary.value.map((persona) => ({ value: persona.id, label: `人设库：${persona.name}` }))
  ];
  const linked = editing.value?.persona_id;
  if (linked && !personaLibrary.value.some((persona) => persona.id === linked)) {
    options.push({ value: linked, label: "人设库：已删除的人设（保存后改为本群自定义）" });
  }
  return options;
});
// 在绑定状态下手改了人设：解除绑定变成本群自定义，记下是从哪一套改出来的，
// 好提供「写回人设库」。和机器人页的规则一样。
const editedFromGroupPersonaID = ref("");
const personaWriteBackBusy = ref(false);
const editedGroupLibraryPersona = computed(() =>
  editedFromGroupPersonaID.value && !editing.value?.persona_id
    ? personaLibrary.value.find((persona) => persona.id === editedFromGroupPersonaID.value)
    : undefined
);
function detachEditedPersona(): void {
  const current = editing.value;
  if (!current?.persona_id) return;
  editedFromGroupPersonaID.value = current.persona_id;
  current.persona_id = undefined;
}
async function writeBackGroupPersona(): Promise<void> {
  const current = editing.value;
  const target = editedGroupLibraryPersona.value;
  if (!current || !target) return;
  const ok = await askConfirm({
    title: `更新人设「${target.name}」`,
    message: "把本群现在的人设内容写回人设库的这一套。所有绑定它的机器人和群都会改成这份内容，并立即生效。",
    confirmLabel: "更新"
  });
  if (!ok) return;
  personaWriteBackBusy.value = true;
  try {
    const response = await savePersona({
      ...target,
      system_prompt: current.system_prompt ?? "",
      self_reference: current.self_reference ?? "",
      sentence_enders: current.sentence_enders ?? "",
      action_description_enabled: current.action_description_enabled ?? false
    });
    personaLibrary.value = response.personas ?? personaLibrary.value;
    if (editing.value === current) current.persona_id = response.persona.id;
    editedFromGroupPersonaID.value = "";
    const synced = [
      response.bots_synced ? `${response.bots_synced} 台机器人` : "",
      response.groups_synced ? `${response.groups_synced} 个群` : ""
    ].filter(Boolean).join("、");
    toastSuccess(synced ? `已更新「${target.name}」，同步到 ${synced}` : `已更新「${target.name}」`);
    if (response.warning) toastError(response.warning);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "人设更新失败");
  } finally {
    personaWriteBackBusy.value = false;
  }
}
function clearLinkedPersonaFields(config: BotGroupConfig): void {
  config.persona_id = undefined;
  config.system_prompt = "";
  config.self_reference = "";
  config.sentence_enders = "";
  config.action_description_enabled = undefined;
}
function setPersonaSource(value: string): void {
  const current = editing.value;
  if (!current || value === personaSource.value) return;
  if (value === "") {
    // 跟随机器人：本群不留任何人设覆盖。绑定带进来的自称、语气词也一并清掉。
    if (current.persona_id) clearLinkedPersonaFields(current);
    else current.system_prompt = "";
    return;
  }
  if (value === "custom") {
    // 解除绑定、保留现有文字，从这里开始手改。
    current.persona_id = undefined;
    if (!current.system_prompt?.trim()) current.system_prompt = inheritedPersona.value;
    return;
  }
  const persona = personaLibrary.value.find((item) => item.id === value);
  if (!persona) return;
  // 先在表单里预览；保存时后端按绑定用人设库里的内容为准。
  current.persona_id = persona.id;
  current.system_prompt = persona.system_prompt ?? "";
  current.self_reference = persona.self_reference ?? "";
  current.sentence_enders = persona.sentence_enders ?? "";
  current.action_description_enabled = persona.action_description_enabled ?? false;
}
const personaPlaceholder = computed(() =>
  inheritedPersona.value
    ? `留空跟随${inheritedPersonaOwner.value}：${truncate(inheritedPersona.value, 40)}`
    : `留空跟随${inheritedPersonaOwner.value}的人设；同一个机器人可以在不同群扮演不同角色`
);
const defaultRecallReplyAutoDeleteDelaySeconds = 60;
const maximumRecallReplyAutoDeleteDelaySeconds = 60 * 60;
const defaultRecallReplyAutoDeleteDelay = ref(defaultRecallReplyAutoDeleteDelaySeconds);

const filteredGroups = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase();
  if (!query) {
    return groups.value;
  }
  return groups.value.filter((group) => group.group_id.includes(query) || group.group_name?.toLocaleLowerCase().includes(query));
});
const joinedCount = computed(() => groups.value.filter((group) => group.joined).length);
const configuredCount = computed(() => groups.value.filter((group) => group.configured).length);

function truncate(text: string, max: number): string {
  return text.length > max ? text.slice(0, max) + "…" : text;
}

function groupReplyDesireValue(config: BotGroupConfig): string {
  if (config.participation) return participationPresetName(config.participation);
  if (config.natural_interjection_enabled) return "max";
  if (config.chat_in_level) return config.chat_in_level;
  return ({ quiet: "off", assistant: "low", standard: "low", active: "high", super_active: "max" } as Record<string, string>)[config.response_mode ?? ""] ?? "";
}

function participationSummary(p: ParticipationPreferences): string {
  // 档位名后面跟上评分门槛，门槛数字统一来自 participation.ts。
  const name = (level: string) => participationLevelLabel(level, { compact: true });
  const legacy = participationPresetName(p);
  const relevanceOn = (p.relevance_level ?? (legacy === "off" ? "off" : "on")) !== "off";
  return `回应提问 ${relevanceOn ? "开" : "关"} · 闲聊 ${name(p.chat_level ?? (legacy === "max" ? "always" : legacy))}`;
}

function setGroupParticipation(value: ParticipationPreferences | undefined): void {
  if (!editing.value) return;
  editing.value.response_mode = value ? "custom" : "";
  editing.value.chat_in_level = undefined;
  editing.value.chat_in_enabled = undefined;
  editing.value.natural_interjection_enabled = undefined;
  editing.value.chat_in_threshold = undefined;
  editing.value.chat_in_chance = undefined;
  editing.value.participation = value;
}


function overrideCount(group: BotGroupConfig): number {
  return new Set([
    ...Object.keys(group.plugin_overrides ?? {}),
    ...Object.keys(group.plugin_setting_overrides ?? {})
  ]).size;
}

function blockedUserCount(group: BotGroupConfig): number {
  return group.reply_gate?.blocked_users?.length ?? 0;
}

function hasOtherReplyGateRules(group: BotGroupConfig): boolean {
  const gate = group.reply_gate;
  if (!gate) {
    return false;
  }
  return Boolean(
    gate.user_admission === "whitelist" ||
      (gate.allowed_users?.length ?? 0) > 0 ||
    (gate.min_group_level ?? 0) > 0 ||
      (gate.exempt_users?.length ?? 0) > 0 ||
      gate.owner_bypass === false ||
      gate.quiet_reply?.trim()
  );
}

async function load(showFeedback = false): Promise<void> {
  refreshing.value = true;
  try {
    const [response, configAndPlatforms, extensionList, personaList] = await Promise.all([
      listBotGroups(showFeedback, botScope.value),
      Promise.all([getBotProfileConfig(), getBotPlatforms()]).catch(() => null),
      // 扩展目录和群列表互不依赖：取不到就不显示这一栏，不拖累整页。
      botScope.value ? listManagedExtensions(botScope.value).catch(() => null) : Promise.resolve(null),
      listPersonas().catch(() => null)
    ]);
    groups.value = response.groups;
    personaLibrary.value = personaList?.personas ?? [];
    plugins.value = response.plugins;
    extensions.value = extensionList?.items ?? [];
    liveAvailable.value = response.live_available;
    syncWarning.value = response.warning ?? "";
    connectionPeers.value = response.connection_peers ?? [];
    quotaWindowSeconds.value = response.quota_window_seconds ?? 0;
    if (showFeedback) {
      if (response.live_available) {
        toastSuccess(`已同步 ${response.groups.filter((group) => group.joined).length} 个群`);
      } else {
        toastError(response.warning ?? "暂时无法同步群列表");
      }
    }
    if (configAndPlatforms) {
      const [config, platformList] = configAndPlatforms;
      const active = config.profiles?.find((profile) => profile.id === botScope.value) ?? config.profiles?.[0];
      const current = active ?? config;
      markedBotDefaults.value = Object.fromEntries([
        ["",current.marked_bot_ids ?? []],
        ...(config.profiles ?? []).map(profile=>[profile.id,profile.marked_bot_ids ?? []])
      ]);
      participationDefaults.value = Object.fromEntries([
        ["", participationFromConfig(current)],
        ...(config.profiles ?? []).map(profile => [profile.id, participationFromConfig(profile)])
      ]);
      personaDefaults.value = Object.fromEntries([
        ["", { name: current.name ?? "", prompt: current.system_prompt ?? "" }],
        ...(config.profiles ?? []).map((profile) => [profile.id, { name: profile.name ?? "", prompt: profile.system_prompt ?? "" }])
      ]);
      newGroupEnabled.value = (current.group_admission?.mode ?? "blacklist") !== "whitelist";
      defaultMinGroupLevel.value = current.reply_gate?.min_group_level ?? 0;
      defaultLevelUnknownPolicy.value = current.reply_gate?.level_unknown_policy === "deny" ? "deny" : "allow";
      defaultRecallReplyAutoDeleteEnabled.value = current.recall_reply_auto_delete_enabled ?? false;
      naturalReplySplitDefaults.value = Object.fromEntries([
        ["", current.natural_reply_split_enabled ?? true],
        ...(config.profiles ?? []).map((profile) => [profile.id, profile.natural_reply_split_enabled ?? true])
      ]);
      lineSplitDefaults.value = Object.fromEntries([
        ["", current.reply_line_split_enabled ?? false],
        ...(config.profiles ?? []).map((profile) => [profile.id, profile.reply_line_split_enabled ?? false])
      ]);
      typingDelayDefaults.value = Object.fromEntries([
        ["", current.typing_delay_enabled ?? false],
        ...(config.profiles ?? []).map((profile) => [profile.id, profile.typing_delay_enabled ?? false])
      ]);
      defaultSocialReplyEnabled.value = current.social_reply_enabled ?? false;
      mutedReplyPauseDefaults.value = Object.fromEntries([
        ["", current.muted_reply_pause_enabled ?? true],
        ...(config.profiles ?? []).map((profile) => [profile.id, profile.muted_reply_pause_enabled ?? true])
      ]);
      for (const item of mutedStepItems) {
        mutedStepDefaults.value[item.key] = Object.fromEntries([
          ["", current[item.key] ?? item.fallback],
          ...(config.profiles ?? []).map((profile) => [profile.id, profile[item.key] ?? item.fallback])
        ]);
      }
      sendRetryDefaults.value = Object.fromEntries([["", current], ...(config.profiles ?? []).map((profile) => [profile.id, profile])]);
      defaultRecallReplyAutoDeleteDelay.value = current.recall_reply_auto_delete_delay_seconds ?? defaultRecallReplyAutoDeleteDelaySeconds;
      const def = platformList.platforms.find((item) => item.id === active?.platform);
      supportsGroupLevel.value = def ? def.protocol.startsWith("onebot") : true;
    } else {
      // 拿不到平台信息时保守地把等级门槛显示出来。
      supportsGroupLevel.value = true;
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "读取群配置失败");
  } finally {
    loaded.value = true;
    refreshing.value = false;
  }
}

function addGroup(): void {
  const groupID = newGroupID.value.trim();
  if (!/^\d{5,12}$/.test(groupID)) {
    toastError("请输入正确的群号");
    return;
  }
  const existing = groups.value.find((group) => group.group_id === groupID);
  openEditor(
    existing ?? {
      group_id: groupID,
      enabled: true,
      group_triggers: [],
      social_reply_enabled: defaultSocialReplyEnabled.value,
      recall_reply_auto_delete_enabled: defaultRecallReplyAutoDeleteEnabled.value,
      recall_reply_auto_delete_delay_seconds: defaultRecallReplyAutoDeleteDelay.value,
      plugin_overrides: {},
      plugin_setting_overrides: {}
    },
    existing?.group_name
  );
  newGroupID.value = "";
}

function openEditor(group: BotGroupConfig, groupName = ""): void {
  // 深拷贝编辑，取消时不污染列表数据。
  const config = JSON.parse(JSON.stringify(groupConfigOf(group))) as BotGroupConfig;
  if (!config.participation && groupReplyDesireValue(config)) config.participation = participationFromConfig(config);
  config.recall_reply_auto_delete_enabled ??= defaultRecallReplyAutoDeleteEnabled.value;
  config.social_reply_enabled ??= defaultSocialReplyEnabled.value;
  config.plugin_setting_overrides ??= {};
  config.response_mode ??= "";
  withUnsetSendRetryCleared(config);
  editedFromGroupPersonaID.value = "";
  const delay = Number(config.recall_reply_auto_delete_delay_seconds);
  config.recall_reply_auto_delete_delay_seconds = Number.isInteger(delay) && delay > 0 ? delay : defaultRecallReplyAutoDeleteDelay.value;
  editing.value = config;
  editingGroupName.value = groupName;
  triggersDraft.value = (group.group_triggers ?? []).join(",");
  welcomeTemplatesDraft.value = (config.welcome_templates ?? []).join("\n");
}

function groupConfigOf(group: BotGroupConfig): BotGroupConfig {
  const summary = group as Partial<BotGroupSummary>;
  const { group_name, avatar_url, member_count, max_member_count, configured, joined, ...config } = summary;
  return config as BotGroupConfig;
}

function hideBrokenAvatar(event: Event): void {
  (event.currentTarget as HTMLImageElement).hidden = true;
}

// 机器人那一档由扩展页的启用开关和成员档位推出来，和后端 BotExtensionTier 同一套规则。
function botTierOf(item: ManagedExtension): string {
  if (!item.enabled || item.available === false) {
    return "off";
  }
  if (!item.members_enabled) {
    return "owner";
  }
  return item.member_audience?.min_role === "admin" ? "admins" : "members";
}

function extensionTierLabel(tier: string): string {
  return extensionTiers.find((item) => item.value === tier)?.label ?? "仅主人";
}

async function resolveAccountNames(ids: string[]): Promise<Record<string, string>> {
  const response = await fetchAssistantUserNames(ids);
  return response.names ?? {};
}

function accessOf(extensionID: string): { tier?: string; allow?: string[]; deny?: string[] } {
  return editing.value?.extension_access?.[extensionID] ?? {};
}

function tierOf(extensionID: string): string | undefined {
  return accessOf(extensionID).tier || undefined;
}

function listOf(extensionID: string, kind: "allow" | "deny"): string[] {
  return accessOf(extensionID)[kind] ?? [];
}

function patchAccess(extensionID: string, patch: { tier?: string; allow?: string[]; deny?: string[] }): void {
  if (!editing.value) {
    return;
  }
  const access = { ...(editing.value.extension_access ?? {}) };
  const next = { ...(access[extensionID] ?? {}), ...patch };
  if (!next.tier && !(next.allow ?? []).length && !(next.deny ?? []).length) {
    delete access[extensionID];
  } else {
    access[extensionID] = next;
  }
  editing.value.extension_access = access;
}

function setTier(extensionID: string, tier: string | undefined): void {
  // 切回跟随就把本群那两份名单一起清掉：留着看不见的名单，下次改档位会莫名其妙生效。
  if (!tier || tier === "off") {
    patchAccess(extensionID, { tier: tier ?? "", allow: [], deny: [] });
    return;
  }
  patchAccess(extensionID, { tier });
}

function setList(extensionID: string, kind: "allow" | "deny", accounts: string[]): void {
  patchAccess(extensionID, kind === "allow" ? { allow: accounts } : { deny: accounts });
}

function overrideOf(pluginID: string): boolean | undefined {
  return editing.value?.plugin_overrides?.[pluginID];
}

function setOverride(pluginID: string, value: boolean | undefined): void {
  if (!editing.value) {
    return;
  }
  const overrides = { ...(editing.value.plugin_overrides ?? {}) };
  if (value === undefined) {
    delete overrides[pluginID];
  } else {
    overrides[pluginID] = value;
  }
  editing.value.plugin_overrides = overrides;
}

function settingOverridesOf(pluginID: string): Record<string, unknown> {
  return editing.value?.plugin_setting_overrides?.[pluginID] ?? {};
}

function setPluginSettingOverrides(pluginID: string, values: Record<string, unknown>): void {
  if (!editing.value) {
    return;
  }
  const overrides = { ...(editing.value.plugin_setting_overrides ?? {}) };
  if (Object.keys(values).length === 0) {
    delete overrides[pluginID];
  } else {
    overrides[pluginID] = values;
  }
  editing.value.plugin_setting_overrides = overrides;
}

// 新群默认和一键开关都写在机器人这一侧：前者是「没有群配置的群怎么办」，
// 后者只作用于当前列出来的群，两者合起来才是完整的一份逐群开关。
const newGroupEnabled = ref(true);
const bulkBusy = ref(false);

const defaultMinGroupLevel = ref(0);
const defaultLevelUnknownPolicy = ref<"allow" | "deny">("allow");
const levelUnknownOptions: AppSelectOption[] = [
  { value: "allow", label: "放行（推荐）", hint: "读不到等级时照常回复" },
  { value: "deny", label: "拦截", hint: "读不到等级时不回复" }
];

async function saveMinGroupLevel(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const level = Math.max(0, Math.trunc(Number(input.value)) || 0);
  if (level === defaultMinGroupLevel.value) {
    input.value = String(level);
    return;
  }
  bulkBusy.value = true;
  try {
    await saveBotGroupSwitches({ bot_profile_id: botScope.value, min_group_level: level });
    defaultMinGroupLevel.value = level;
    input.value = String(level);
    toastSuccess(level > 0 ? `群等级门槛设为 Lv.${level}` : "群等级门槛已取消");
  } catch (error) {
    input.value = String(defaultMinGroupLevel.value);
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    bulkBusy.value = false;
  }
}

async function saveLevelUnknownPolicy(policy: "allow" | "deny"): Promise<void> {
  if (policy === defaultLevelUnknownPolicy.value) {
    return;
  }
  bulkBusy.value = true;
  try {
    await saveBotGroupSwitches({ bot_profile_id: botScope.value, level_unknown_policy: policy });
    defaultLevelUnknownPolicy.value = policy;
    toastSuccess(policy === "allow" ? "等级读不到时放行" : "等级读不到时拦截");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    bulkBusy.value = false;
  }
}

function sharedBotsTitle(group: BotGroupSummary): string {
  const names = (group.shared_with ?? []).map((bot) => `「${bot.name || "未命名机器人"}」`).join("");
  return `${names}复用同一条连接，在这个群也开着：群里会收到多份回复。要只留一台说话，把别的台在这个群关掉。`;
}

async function setNewGroupDefault(event: Event): Promise<void> {
  const enabled = (event.target as HTMLInputElement).checked;
  bulkBusy.value = true;
  try {
    const result = await saveBotGroupSwitches({ bot_profile_id: botScope.value, new_group_enabled: enabled });
    newGroupEnabled.value = enabled;
    toastSuccess(enabled ? "新加入的群默认工作" : "新加入的群默认不工作");
    if (result.warning) toastError(result.warning);
  } catch (error) {
    (event.target as HTMLInputElement).checked = !enabled;
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    bulkBusy.value = false;
  }
}

async function setAllGroups(enabled: boolean): Promise<void> {
  const groupIDs = filteredGroups.value.map((group) => group.group_id).filter(Boolean);
  if (!groupIDs.length) {
    return;
  }
  const ok = await askConfirm({
    title: enabled ? "全部启用" : "全部停用",
    message: `确定把下面列出的 ${groupIDs.length} 个群${enabled ? "全部启用" : "全部停用"}吗？新加入的群不受影响，仍按「新群默认」决定。`,
    confirmLabel: enabled ? "全部启用" : "全部停用",
    danger: !enabled
  });
  if (!ok) {
    return;
  }
  bulkBusy.value = true;
  try {
    const result = await saveBotGroupSwitches({ bot_profile_id: botScope.value, group_ids: groupIDs, enabled });
    toastSuccess(`已${enabled ? "启用" : "停用"} ${result.updated} 个群`);
    if (result.warning) toastError(result.warning);
    await load();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    bulkBusy.value = false;
  }
}

async function toggleGroup(group: BotGroupSummary, event: Event): Promise<void> {
  const enabled = (event.target as HTMLInputElement).checked;
  togglingGroupID.value = group.group_id;
  try {
    const saved = await saveBotGroup({ ...groupConfigOf(group), bot_profile_id: botScope.value || group.bot_profile_id, enabled });
    upsert(saved.config);
    toastSuccess(enabled ? `群 ${group.group_id} 已启用` : `群 ${group.group_id} 已停用`);
    if (saved.warning) toastError(saved.warning);
    // 冲突提示来自别的机器人的配置，本页那枚「同连接 N 台都在回」的角标也得跟着变。
    await load();
  } catch (error) {
    (event.target as HTMLInputElement).checked = !enabled;
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    togglingGroupID.value = "";
  }
}

async function saveEditing(): Promise<void> {
  const current = editing.value;
  if (!current) {
    return;
  }
  const recallDeleteDelay = Number(current.recall_reply_auto_delete_delay_seconds);
  if (
    current.recall_reply_auto_delete_enabled &&
    (!Number.isInteger(recallDeleteDelay) || recallDeleteDelay < 1 || recallDeleteDelay > maximumRecallReplyAutoDeleteDelaySeconds)
  ) {
    toastError(`回复保留时间请输入 1 到 ${maximumRecallReplyAutoDeleteDelaySeconds} 秒之间的整数`);
    return;
  }
  const sendRetryError = sendRetryValidationError(current);
  if (sendRetryError) {
    toastError(sendRetryError);
    return;
  }
  saving.value = true;
  try {
    const payload: BotGroupConfig = {
      ...current,
      ...sendRetryPayload(current),
      forward_reply_threshold: Number(current.forward_reply_threshold) || 0,
      // 数字框清空后 v-model.number 给的是空串，后端按整数解析会整份拒收。
      model_call_quota: Math.max(0, Math.round(Number(current.model_call_quota) || 0)),
      reply_sample_percent: Math.min(100, Math.max(0, Math.round(Number(current.reply_sample_percent) || 0))),
      forward_reply_chunk_threshold: Number(current.forward_reply_chunk_threshold) || 0,
      reply_merge_confidence_percent: Number(current.reply_merge_confidence_percent) || 0,
      recall_reply_auto_delete_delay_seconds: Number.isInteger(recallDeleteDelay)
        ? recallDeleteDelay
        : defaultRecallReplyAutoDeleteDelaySeconds,
      group_triggers: triggersDraft.value
        .split(/[,，]/)
        .map((item) => item.trim())
        .filter((item) => item !== ""),
      welcome_templates: welcomeTemplatesDraft.value
        .split("\n")
        .map((item) => item.trim())
        .filter((item) => item !== ""),
      welcome_llm_cooldown_seconds: Number(current.welcome_llm_cooldown_seconds) || 0
    };
    const saved = await saveBotGroup({ ...payload, bot_profile_id: botScope.value || payload.bot_profile_id });
    upsert(saved.config);
    editing.value = null;
    toastSuccess(`群 ${payload.group_id} 配置已保存`);
    if (saved.warning) toastError(saved.warning);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

function upsert(config: BotGroupConfig): void {
  const index = groups.value.findIndex((group) => group.group_id === config.group_id);
  if (index >= 0) {
    groups.value[index] = {
      ...groups.value[index],
      ...config,
      // 恢复继承时响应会省略这个字段，不能保留列表里先前的显式开关。
      natural_reply_split_enabled: config.natural_reply_split_enabled,
      reply_preserve_line_breaks: config.reply_preserve_line_breaks,
      reply_line_split_enabled: config.reply_line_split_enabled,
      typing_delay_enabled: config.typing_delay_enabled,
      configured: true
    };
  } else {
    // 头像地址由后端按平台决定（QQ 直链或本机代理），前端不再自己拼；
    // 这里先留空，下一次拉取列表时补上。
    groups.value.push({
      ...config,
      configured: true,
      joined: false
    });
  }
}

// 换了机器人，群列表和每个群的配置都是另一套。
watch(botScope, () => {
  editing.value = null;
  pendingDelete.value = null;
  void load();
});

onMounted(() => load());
useConfigurationRefresh(["bot"], () => load());

</script>

<style scoped>
/* 跟 BotMarkerList 的「机器人范围」一样，是一行不抢眼的继承说明。 */
.inherited-persona {
  font-size: 12px;
  color: var(--muted);
}
.inherited-persona > summary {
  cursor: pointer;
}
.inherited-persona > p {
  margin: 6px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.group-extension-lists{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:10px}
@media(max-width:700px){.group-extension-lists{grid-template-columns:1fr}}
.quota-usage {
  display: grid;
  gap: 6px;
}

.quota-track {
  height: 7px;
  overflow: hidden;
  background: var(--surface-muted);
  border: 1px solid var(--border);
  border-radius: 4px;
}

.quota-track span {
  display: block;
  height: 100%;
  min-width: 2px;
  background: var(--accent);
  transition: width 180ms ease;
}

.quota-track span.full {
  background: var(--danger);
}
</style>
