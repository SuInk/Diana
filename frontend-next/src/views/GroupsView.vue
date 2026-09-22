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
            <span v-if="group.configured && group.system_prompt" class="badge">专属人设</span>
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
          <label for="group-prompt">本群专属人设（留空跟随{{ inheritedPersonaOwner }}的人设）</label>
          <textarea
            id="group-prompt"
            v-model="editing.system_prompt"
            class="textarea"
            rows="3"
            :placeholder="personaPlaceholder"
          ></textarea>
          <span class="hint">留空表示本群一直跟着{{ inheritedPersonaOwner }}走，改机器人人设时本群也跟着变；填了就只用这里的文字。</span>
          <details v-if="inheritedPersona" class="inherited-persona">
            <summary>{{ inheritedPersonaOwner }}当前的人设</summary>
            <p>{{ inheritedPersona }}</p>
          </details>
        </div>
        <div class="field">
          <label>接话设置</label>
          <ParticipationControls :key="`${editing.bot_profile_id}:${editing.group_id}`" :model-value="editing.participation" :level="groupReplyDesireValue(editing)" :inherited-value="participationDefaults[editing.bot_profile_id || botScope || '']" inheritable @update:model-value="setGroupParticipation" />
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
            @update:model-value="(value) => { if (editing) editing.action_description_enabled = value === '' ? undefined : value === 'on'; }"
          />
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
          <label for="group-natural-split">本群允许多条发送</label>
          <AppSelect
            id="group-natural-split"
            :model-value="editing.natural_reply_split_enabled == null ? '' : editing.natural_reply_split_enabled ? 'on' : 'off'"
            :options="groupNaturalReplySplitOptions"
            @update:model-value="(value) => { if (editing) editing.natural_reply_split_enabled = value === '' ? undefined : value === 'on'; }"
          />
          <span class="hint">换行不分条；开启后只认显式分条标记，关闭后单条发送、超限压缩。本轮用户明确要求优先。</span>
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
  type ManagedExtension,
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
const groupNaturalReplySplitOptions = computed<AppSelectOption[]>(() => [
  { value: "", label: `跟随机器人（${defaultNaturalReplySplitEnabled.value ? "开启" : "关闭"}）` },
  { value: "on", label: "开启" },
  { value: "off", label: "关闭" }
]);
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
    const [response, configAndPlatforms, extensionList] = await Promise.all([
      listBotGroups(showFeedback, botScope.value),
      Promise.all([getBotProfileConfig(), getBotPlatforms()]).catch(() => null),
      // 扩展目录和群列表互不依赖：取不到就不显示这一栏，不拖累整页。
      botScope.value ? listManagedExtensions(botScope.value).catch(() => null) : Promise.resolve(null)
    ]);
    groups.value = response.groups;
    plugins.value = response.plugins;
    extensions.value = extensionList?.items ?? [];
    liveAvailable.value = response.live_available;
    syncWarning.value = response.warning ?? "";
    connectionPeers.value = response.connection_peers ?? [];
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
      defaultSocialReplyEnabled.value = current.social_reply_enabled ?? false;
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
  saving.value = true;
  try {
    const payload: BotGroupConfig = {
      ...current,
      forward_reply_threshold: Number(current.forward_reply_threshold) || 0,
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
</style>
