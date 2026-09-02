<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="plugins-view">
    <header class="view-header plugins-view-header">
      <div class="view-title">
        <h1>插件</h1>
        <p>管理内置能力的启停与详细设置</p>
      </div>
      <div class="view-actions">
        <div class="plugin-search">
          <Search :size="14" aria-hidden="true" />
          <input
            v-model="query"
            class="input"
            type="search"
            placeholder="搜索插件名称或说明"
            aria-label="搜索插件"
          />
        </div>
        <!-- 语义和计数样式对齐事件页的同类筛选器：单选组 + 等宽数字 -->
        <div class="segmented plugin-status-filter" role="radiogroup" aria-label="按状态筛选">
          <button type="button" role="radio" :aria-checked="status === 'all'" :class="{ active: status === 'all' }" @click="status = 'all'">
            <span>全部</span>
            <span class="plugin-filter-count">{{ displayPlugins.length }}</span>
          </button>
          <button type="button" role="radio" :aria-checked="status === 'on'" :class="{ active: status === 'on' }" @click="status = 'on'">
            <span>已启用</span>
            <span class="plugin-filter-count">{{ enabledCount }}</span>
          </button>
          <button type="button" role="radio" :aria-checked="status === 'off'" :class="{ active: status === 'off' }" @click="status = 'off'">
            <span>已停用</span>
            <span class="plugin-filter-count">{{ displayPlugins.length - enabledCount }}</span>
          </button>
        </div>
        <div class="segmented plugin-layout-switch" role="group" aria-label="插件排列方式">
          <button
            type="button"
            :class="{ active: layout === 'tiles' }"
            title="方块：一行一行往右排"
            aria-label="方块排列"
            @click="setLayout('tiles')"
          >
            <LayoutGrid :size="14" aria-hidden="true" />
          </button>
          <button
            type="button"
            :class="{ active: layout === 'rows' }"
            title="横排：一行一个插件，信息更紧凑"
            aria-label="横排排列"
            @click="setLayout('rows')"
          >
            <Rows3 :size="14" aria-hidden="true" />
          </button>
        </div>
        <!-- 刷新不是第三种排列方式，从分段控件里拿出来，和其它页面写法一致 -->
        <button class="btn" type="button" :disabled="loading" @click="reload">
          <RefreshCw :size="15" :class="{ spin: loading }" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <div v-if="visiblePlugins.length > 0" :class="layout === 'rows' ? 'plugin-rows' : 'plugin-tiles'">
      <article
        v-for="plugin in visiblePlugins"
        :key="plugin.manifest.id"
        class="plugin-card"
        :class="{ off: plugin.installed && !pluginEnabled(plugin), uninstalled: !plugin.installed }"
      >
        <div class="plugin-card-head">
          <h2 class="plugin-card-name">{{ pluginDisplayName(plugin) }}</h2>
          <label
            v-if="plugin.installed"
            class="switch"
            :title="pluginEnabled(plugin) ? '点击停用' : '点击启用'"
          >
            <input
              type="checkbox"
              :checked="pluginEnabled(plugin)"
              :disabled="busyID === plugin.manifest.id"
              @change="toggleEnabled(plugin)"
            />
            <span class="track" aria-hidden="true"></span>
          </label>
        </div>

        <div class="cluster plugin-card-badges">
          <!-- 官方 + 内置目前是全部插件的共同属性，逐张重复没有信息量；
               只在例外时标注，第三方插件出现后这里才会有内容。 -->
          <span v-if="!plugin.manifest.official" class="badge warn">第三方</span>
          <span v-if="!plugin.manifest.built_in" class="badge">可卸载</span>
          <!-- 别的内置插件装好就在跑，这张卡片的开关却是关的。不说一句，
               看起来就像是它坏了。 -->
          <span v-if="plugin.manifest.default_disabled" class="badge">默认关闭</span>
          <span class="badge mono">v{{ plugin.manifest.version }}</span>
        </div>

        <p class="plugin-card-desc" :title="pluginDisplayDescription(plugin)">{{ pluginDisplayDescription(plugin) }}</p>

        <div v-if="plugin.manifest.permissions?.length || showFooter(plugin)" class="plugin-card-bottom">
          <!-- 权限在左，设置等操作在右；有无设置都不再改变卡片的基础高度。 -->
          <div class="plugin-card-meta">
            <!-- 依赖列表展开后比整张卡片还高，行内展开会把这一条撑得和邻居完全
                 不是一个量级；改成弹窗，卡片上只留状态。
                 排在权限前面：依赖缺了插件直接不工作，比权限更需要先被看到。 -->
            <button
              v-if="dependenciesFor(plugin.manifest.id).length"
              class="plugin-dependencies-head"
              type="button"
              title="查看运行依赖"
              @click="openDependencies(plugin)"
            >
              <span>运行依赖</span>
              <!-- 缺依赖等于这个插件直接不工作，这条得能在一屏插件里被一眼扫到 -->
              <span
                class="plugin-dependency-count"
                :class="{ warn: hasDependencyProblem(plugin.manifest.id) }"
              >
                {{ readyDependencyCount(plugin.manifest.id) }}/{{ dependenciesFor(plugin.manifest.id).length }}
              </span>
            </button>

            <!-- 和运行依赖一样走弹窗：权限标签展开后会把这一行顶高一截，
                 一列卡片的高度就不齐了。 -->
            <button
              v-if="plugin.manifest.permissions?.length"
              class="plugin-perms-head"
              type="button"
              title="查看权限"
              @click="permissionsTarget = plugin"
            >
              {{ plugin.manifest.permissions.length }} 项权限
            </button>
          </div>

          <footer v-if="showFooter(plugin)" class="plugin-card-foot">
            <template v-if="plugin.installed">
              <button
                v-if="plugin.manifest.settings?.length"
                class="btn small"
                type="button"
                :disabled="busyID === plugin.manifest.id"
                @click="openSettings(plugin)"
              >
                <SlidersHorizontal :size="14" aria-hidden="true" />
                设置
              </button>
              <button
                v-if="!plugin.manifest.built_in"
                class="btn small ghost danger"
                type="button"
                :disabled="busyID === plugin.manifest.id"
                @click="uninstall(plugin)"
              >
                卸载
              </button>
            </template>
            <button
              v-else
              class="btn small primary"
              type="button"
              :disabled="busyID === plugin.manifest.id"
              @click="install(plugin)"
            >
              安装
            </button>
          </footer>
        </div>
      </article>
    </div>
    <EmptyState
      v-else-if="!loading && displayPlugins.length > 0"
      title="没有匹配的插件"
      hint="换个关键词，或把筛选切回「全部」。"
    />
    <EmptyState v-else-if="!loading" title="没有可用插件" />
    <div v-else class="plugin-grid">
      <div v-for="n in 3" :key="n" class="skeleton" style="height: 190px; border-radius: var(--radius-lg)"></div>
    </div>

    <Modal
      v-if="settingsTarget"
      :title="isGitHubSettings ? 'GitHub 仓库 · 设置' : `${settingsTarget.manifest.name} · 设置`"
      :wide="settingsTarget.manifest.id === repositoryWatchPluginID || settingsTarget.manifest.id === repositoryPublishPluginID || settingsTarget.manifest.id === rssWatchPluginID || settingsTarget.manifest.id === musicPluginID"
      @close="closeSettings"
    >
      <!-- 依赖多数时候是齐的，默认折叠把弹窗顶部让给真正要改的设置项；
           缺依赖时自动展开，那才是需要立刻处理的状态。 -->
      <details
        v-if="dependenciesFor(settingsTarget.manifest.id).length"
        class="plugin-settings-section-head plugin-settings-collapsible"
        :open="hasDependencyProblem(settingsTarget.manifest.id)"
      >
        <summary>
          <h3>运行依赖</h3>
          <span class="badge" :class="hasDependencyProblem(settingsTarget.manifest.id) ? 'warn' : 'accent'">
            {{ readyDependencyCount(settingsTarget.manifest.id) }}/{{ dependenciesFor(settingsTarget.manifest.id).length }}
          </span>
          <ChevronDown class="plugin-settings-chevron" :size="15" aria-hidden="true" />
        </summary>
        <p>{{ dependencyHint(settingsTarget.manifest.id) }}</p>
        <PluginDependencyList
          :dependencies="dependenciesFor(settingsTarget.manifest.id)"
          :loading="dependenciesLoading"
          :busy="busyDependency"
          @install="installDependency"
        />
      </details>

      <div v-if="isGitHubSettings" class="segmented github-settings-tabs" role="tablist" aria-label="GitHub 仓库设置">
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'config'" :class="{ active: githubSettingsTab === 'config' }" @click="githubSettingsTab = 'config'">配置信息</button>
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'repositories'" :class="{ active: githubSettingsTab === 'repositories' }" @click="githubSettingsTab = 'repositories'">仓库管理</button>
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'records'" :class="{ active: githubSettingsTab === 'records' }" @click="githubSettingsTab = 'records'">运行记录</button>
      </div>

      <template v-if="isGitHubSettings && githubSettingsTab === 'config'">
        <div class="plugin-settings-section-head">
          <h3>GitHub 认证</h3>
          <p>
            公共 Token 同时用于仓库更新检查和 Issue 创建；具体仓库是否允许 Issue 操作，在「仓库管理」中配置。
            公开仓库也可以匿名读取，但请求额度较低。
            <a class="token-create-link" href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noreferrer"><ExternalLink :size="13" aria-hidden="true" />创建 Token</a>
          </p>
        </div>
        <!-- Token 是这一页的主角，排在最前；认证方式和凭据列表都是围绕它的补充。 -->
        <div class="stack plugin-settings-form">
          <PluginSettingField
            v-for="spec in githubTokenSpecs"
            :key="spec.key"
            :spec="spec"
            :form="settingsForm"
            :clearing="clearSecrets.includes(spec.key)"
            :secret-configured="secretConfigured(spec.key)"
            :secret-placeholder="secretPlaceholder(spec.key)"
            @toggle-clear="toggleClearSecret"
          />
          <div v-if="repositoryPublishAuthSpec" class="field">
            <label for="setting-github_auth_mode">{{ repositoryPublishAuthSpec.label }}</label>
            <AppSelect id="setting-github_auth_mode" v-model="repositoryPublishForm.github_auth_mode" :options="repositoryPublishAuthSpec.options ?? []" />
            <span v-if="repositoryPublishAuthSpec.description" class="hint">{{ repositoryPublishAuthSpec.description }}</span>
          </div>
        </div>
        <RepositoryCredentialEditor
          ref="credentialEditor"
          :credentials="credentialList"
          :configured-ids="configuredCredentialIDs"
          :repository-credentials="repositoryCredentialMap"
          @update:credentials="onCredentialsChanged"
          @update:tokens="credentialTokenDrafts = $event"
          @update:repository-credentials="onRepositoryCredentialsChanged"
        />
        <div v-if="githubNotifySpecs.length" class="plugin-settings-section-head plugin-settings-subsection">
          <h3>通知</h3>
          <p>仓库动态推送成什么样，以及要不要让机器人在推送后接一句话。</p>
        </div>
        <div class="stack plugin-settings-form">
          <PluginSettingField
            v-for="spec in githubNotifySpecs"
            :key="spec.key"
            :spec="spec"
            :form="settingsForm"
          />
        </div>
        <div class="plugin-settings-section-head plugin-settings-subsection">
          <h3>运行</h3>
          <p>两个超时管的是不同的事：一个是拉取仓库动态，一个是创建或评论 Issue。</p>
        </div>
        <div class="stack plugin-settings-form">
          <PluginSettingField
            v-for="spec in githubRuntimeSpecs"
            :key="spec.key"
            :spec="spec"
            :form="settingsForm"
          />
          <PluginSettingField
            v-if="repositoryPublishTimeoutSpec"
            :spec="repositoryPublishTimeoutSpec"
            :form="repositoryPublishForm"
            field-id="setting-publish-timeout"
          />
        </div>
        <button class="btn small ghost github-settings-link" type="button" @click="githubSettingsTab = 'repositories'">
          去仓库管理配置订阅、用户和群聊
          <ArrowRight :size="14" aria-hidden="true" />
        </button>
      </template>
      <template v-if="isMusicSettings">
        <div class="plugin-settings-section-head">
          <h3>曲库与会员能力</h3>
          <p>不配置凭据也能尝试公开试听；会员歌曲需要对应平台登录态。凭据只写入服务器，页面不会回显原文。</p>
        </div>
        <div class="music-platform-grid">
          <section v-for="platform in musicPlatforms" :key="platform.key" class="music-platform-card">
            <div class="music-platform-head">
              <div>
                <h4>{{ platform.label }}</h4>
                <span class="hint">{{ musicPlatformSummary(platform.key) }}</span>
              </div>
              <span class="badge" :class="musicStatusClass(platform.key)">{{ musicStatusLabel(platform.key) }}</span>
            </div>
            <p class="music-platform-guide">{{ platform.guide }}</p>
            <PluginSettingField
              v-for="spec in musicSpecsFor(platform.key)"
              :key="spec.key"
              :spec="spec"
              :form="settingsForm"
              :clearing="clearSecrets.includes(spec.key)"
              :secret-configured="secretConfigured(spec.key)"
              :secret-placeholder="secretPlaceholder(spec.key)"
              @toggle-clear="toggleClearSecret"
            />
            <p v-if="musicCredentialHint(platform.key)" class="music-credential-hint">{{ musicCredentialHint(platform.key) }}</p>
          </section>
        </div>
        <button class="btn music-test-button" type="button" :disabled="testingMusic" @click="testMusicSettings">
          <RefreshCw :size="15" :class="{ spin: testingMusic }" aria-hidden="true" />
          {{ testingMusic ? "正在测试三家曲库" : "测试连接与播放能力" }}
        </button>
        <div class="plugin-settings-section-head plugin-settings-subsection">
          <h3>播放设置</h3>
          <p>下面这些设置同时作用于所有已启用曲库。</p>
        </div>
        <div class="stack plugin-settings-form">
          <PluginSettingField v-for="spec in musicGeneralSpecs" :key="spec.key" :spec="spec" :form="settingsForm" />
        </div>
      </template>
      <div v-if="!isGitHubSettings && !isMusicSettings" class="stack plugin-settings-form">
        <PluginSettingField
          v-for="spec in visibleSettingsSpecs"
          :key="spec.key"
          :spec="spec"
          :form="settingsForm"
          :clearing="clearSecrets.includes(spec.key)"
          :secret-configured="secretConfigured(spec.key)"
          :secret-placeholder="secretPlaceholder(spec.key)"
          @toggle-clear="toggleClearSecret"
        />
      </div>
      <RepositoryWatchManager
        v-if="isGitHubSettings && githubSettingsTab === 'repositories'"
        ref="repositoryWatchRef"
        :prepare-access="saveSettingsForSubscription"
        :token-configured="repositoryWatchTokenConfigured"
        :issue-enabled-repositories="issueEnabledRepositories"
        :user-access="String(repositoryPublishForm.user_repository_access ?? '')"
        :group-access="String(repositoryPublishForm.group_repository_access ?? '')"
        :draft-user-access="String(repositoryPublishForm.issue_draft_user_access ?? '')"
        :draft-group-access="String(repositoryPublishForm.issue_draft_group_access ?? '')"
        :manager-user-access="String(repositoryPublishForm.issue_manager_user_access ?? '')"
        :manager-group-access="String(repositoryPublishForm.issue_manager_group_access ?? '')"
        :joined-groups="joinedGroups"
        :groups-loading="groupsLoading"
        :groups-warning="groupsWarning"
        :credentials="credentialList"
        :repository-credentials="repositoryCredentialMap"
        @update:repository-credentials="onRepositoryCredentialsChanged"
        @update:issue-enabled-repositories="repositoryPublishForm.allowed_repositories = $event.join('\n')"
        @update:user-access="repositoryPublishForm.user_repository_access = $event"
        @update:group-access="repositoryPublishForm.group_repository_access = $event"
        @update:draft-user-access="repositoryPublishForm.issue_draft_user_access = $event"
        @update:draft-group-access="repositoryPublishForm.issue_draft_group_access = $event"
        @update:manager-user-access="repositoryPublishForm.issue_manager_user_access = $event"
        @update:manager-group-access="repositoryPublishForm.issue_manager_group_access = $event"
      />
      <div v-if="isGitHubSettings && githubSettingsTab === 'records'" class="github-run-records">
        <div class="plugin-settings-section-head">
          <h3>运行记录</h3>
          <p>Issue 草稿、提出人、日期和详细内容在这里查看；仓库检查和 Issue 创建的操作审计在日志页查看。</p>
        </div>
        <RepositoryIssueDraftList />
        <div class="github-run-records-actions">
          <button class="btn small ghost" type="button" @click="navigate('logs')">查看执行日志</button>
        </div>
      </div>
      <RSSWatchManager
        v-if="settingsTarget.manifest.id === rssWatchPluginID"
        ref="rssWatchRef"
        :prepare-access="saveSettingsForSubscription"
      />
      <template #footer>
        <button class="btn ghost small plugin-settings-reset" type="button" :disabled="savingSettings" @click="resetSettings">
          恢复默认
        </button>
        <button class="btn" type="button" :disabled="savingSettings" @click="closeSettings">取消</button>
        <button class="btn primary" type="button" :disabled="savingSettings" @click="saveSettings">保存</button>
      </template>
    </Modal>

    <Modal
      v-if="permissionsTarget"
      :title="`${permissionsTarget.manifest.name} · 权限`"
      @close="permissionsTarget = null"
    >
      <p class="plugin-dependencies-hint">插件运行时会用到下列能力。</p>
      <div class="cluster plugin-card-perms">
        <span v-for="permission in permissionsTarget.manifest.permissions" :key="permission" class="badge warn">{{ permission }}</span>
      </div>
      <template #footer>
        <button class="btn primary" type="button" @click="permissionsTarget = null">完成</button>
      </template>
    </Modal>

    <Modal
      v-if="dependenciesTarget"
      :title="`${dependenciesTarget.manifest.name} · 运行依赖`"
      @close="dependenciesTarget = null"
    >
      <p class="plugin-dependencies-hint">{{ dependencyHint(dependenciesTarget.manifest.id) }}</p>
      <PluginDependencyList
        :dependencies="dependenciesFor(dependenciesTarget.manifest.id)"
        :loading="dependenciesLoading"
        :busy="busyDependency"
        @install="installDependency"
      />
      <template #footer>
        <button class="btn" type="button" @click="refreshDependencies">重新检测</button>
        <button class="btn primary" type="button" @click="dependenciesTarget = null">完成</button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { ArrowRight, ChevronDown, ExternalLink, LayoutGrid, RefreshCw, Rows3, Search, SlidersHorizontal } from "@lucide/vue";
import {
  installPlugin,
  installResolverDependency,
  listPlugins,
  setPluginEnabled,
  uninstallPlugin,
  updatePluginSettings,
  testMusicConnections,
  listPluginDependencies,
  listBotGroups,
  type PluginSettingSpec,
  type PluginState,
  type ResolverDependency,
  type BotGroupSummary,
  type MusicConnectionStatus
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import AppSelect from "../components/AppSelect.vue";
import PluginSettingField from "../components/PluginSettingField.vue";
import Modal from "../components/Modal.vue";
import RepositoryIssueDraftList from "../components/RepositoryIssueDraftList.vue";
import RepositoryCredentialEditor from "../components/RepositoryCredentialEditor.vue";
import RepositoryWatchManager from "../components/RepositoryWatchManager.vue";
import RSSWatchManager from "../components/RSSWatchManager.vue";
import PluginDependencyList from "../components/PluginDependencyList.vue";
import { navigate, viewQuery } from "../router";

const plugins = ref<PluginState[]>([]);
const loading = ref(false);
const busyID = ref("");

const resolverPluginID = "official.nonebot-plugin-resolver-go";
const sandboxedBrowserPluginID = "official.sandboxed-browser-renderer";
const groupRelationsPluginID = "group_relations";
const repositoryWatchPluginID = "official.repository-watch";
const repositoryPublishPluginID = "official.repository-publish";
const rssWatchPluginID = "official.rss-watch";
const musicPluginID = "official.music";
// 依赖按插件 ID 分组：链接解析要 yt-dlp/ffmpeg/node，网页渲染要一个
// Chrome/Chromium，以后再有别的插件也不必再往模板里加一个 id 判断。
const dependencyGroups = ref<Record<string, ResolverDependency[]>>({});
const dependenciesLoading = ref(false);
const busyDependency = ref("");
const dependenciesTarget = ref<PluginState | null>(null);
const permissionsTarget = ref<PluginState | null>(null);

const dependencyHints: Record<string, string> = {
  [resolverPluginID]: "缺少这些命令时，对应平台的解析会失败；可直接在这里安装。",
  [sandboxedBrowserPluginID]:
    "这里会执行一次真实的本地截图，不再只检查 Chrome 是否安装。浏览器体积不小，安装会比其它依赖慢一些。",
  [groupRelationsPluginID]:
    "关系图有直接字体渲染和浏览器截图两条路径；至少一条检测通过即可正常出图。"
};

function dependenciesFor(pluginID: string): ResolverDependency[] {
  return dependencyGroups.value[pluginID] ?? [];
}

function readyDependencyCount(pluginID: string): number {
  return dependenciesFor(pluginID).filter((dep) => dep.available).length;
}

function missingDependencyCount(pluginID: string): number {
  return dependenciesFor(pluginID).length - readyDependencyCount(pluginID);
}

function hasDependencyProblem(pluginID: string): boolean {
  if (pluginID === groupRelationsPluginID) return readyDependencyCount(pluginID) === 0;
  return missingDependencyCount(pluginID) > 0;
}

function dependencyHint(pluginID: string): string {
  return dependencyHints[pluginID] ?? "缺少这些依赖时，这个插件不会正常工作。";
}

const settingsTarget = ref<PluginState | null>(null);
const repositoryWatchRef = ref<{ hasUnsavedChanges: () => boolean } | null>(null);
const rssWatchRef = ref<{ hasUnsavedChanges: () => boolean } | null>(null);
// 表单值按 spec.type 渲染成对应控件，这里用宽松类型换取模板里干净的 v-model 绑定。
const settingsForm = ref<Record<string, any>>({});
const repositoryPublishForm = ref<Record<string, any>>({});
const clearSecrets = ref<string[]>([]);
// 凭据列表存在订阅插件的设置里：列表和仓库绑定是明文，Token 单独走密钥项、不回显。
const credentialTokenDrafts = ref<Record<string, string>>({});
const credentialEditor = ref<InstanceType<typeof RepositoryCredentialEditor> | null>(null);
const credentialList = computed<Array<{ id: string; name: string; auth: string }>>(() => {
  try {
    const parsed = JSON.parse(String(settingsForm.value.github_credentials ?? "") || "[]");
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
});
const repositoryCredentialMap = computed<Record<string, string>>(() => {
  try {
    const parsed = JSON.parse(String(settingsForm.value.repository_credentials ?? "") || "{}");
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
});
// 已存过 Token 的凭据 ID，用来在输入框显示「已配置 — 留空沿用」。
const configuredCredentialIDs = computed<string[]>(() => {
  try {
    const parsed = JSON.parse(String(settingsForm.value.github_credential_ids ?? "") || "[]");
    return Array.isArray(parsed) ? parsed.map(String) : [];
  } catch {
    return [];
  }
});

function onCredentialsChanged(value: Array<{ id: string; name: string; auth: string }>): void {
  settingsForm.value.github_credentials = JSON.stringify(value);
}

function onRepositoryCredentialsChanged(value: Record<string, string>): void {
  settingsForm.value.repository_credentials = JSON.stringify(value);
}
const savingSettings = ref(false);
const openedSnapshot = ref("");
const githubSettingsTab = ref<"config" | "repositories" | "records">("config");
const joinedGroups = ref<BotGroupSummary[]>([]);
const groupsLoading = ref(false);
const groupsWarning = ref("");

const settingsSpecs = computed<PluginSettingSpec[]>(() => settingsTarget.value?.manifest.settings ?? []);
const repositoryPublishTarget = computed(() => plugins.value.find((plugin) => plugin.manifest.id === repositoryPublishPluginID) ?? null);
const repositoryPublishSpecs = computed<PluginSettingSpec[]>(() => repositoryPublishTarget.value?.manifest.settings ?? []);
const isGitHubSettings = computed(() => settingsTarget.value?.manifest.id === repositoryWatchPluginID);
const isMusicSettings = computed(() => settingsTarget.value?.manifest.id === musicPluginID);
const testingMusic = ref(false);
const musicTestResults = ref<Record<string, MusicConnectionStatus>>({});
const musicPlatforms = [
  { key: "netease", label: "网易云音乐", guide: "公开试听可直接使用；会员歌曲建议填写自建 NeteaseCloudMusicApi，并从浏览器登录 Cookie 中复制 MUSIC_U 的值。" },
  { key: "qq", label: "QQ 音乐", guide: "可直接填写浏览器登录后的完整 Cookie；通常应包含 uin 或 qqmusic_uin，以及 qm_keyst 或 qqmusic_key。自建 API 可选。" },
  { key: "kugou", label: "酷狗音乐", guide: "公开搜索无需配置。会员歌曲需要同时填写自建 KuGouMusicApi 地址和完整 Cookie，Cookie 建议包含 token、userid、dfid。" },
] as const;
const musicCredentialKeys = new Set(musicPlatforms.flatMap((item) => [`${item.key}_api_base`, `${item.key}_cookie`]));
const musicGeneralSpecs = computed(() => settingsSpecs.value.filter((spec) => !musicCredentialKeys.has(spec.key)));

function musicSpecsFor(source: string): PluginSettingSpec[] {
  return settingsSpecs.value.filter((spec) => spec.key === `${source}_api_base` || spec.key === `${source}_cookie`);
}

function musicPlatformSummary(source: string): string {
  const cookieKey = `${source}_cookie`;
  const apiKey = `${source}_api_base`;
  const cookie = !clearSecrets.value.includes(cookieKey) && (secretConfigured(cookieKey) || String(settingsForm.value[cookieKey] ?? "").trim() !== "");
  const api = String(settingsForm.value[apiKey] ?? "").trim() !== "";
  if (source === "kugou" && cookie && !api) return "已填凭据，但会员能力还缺自建 API";
  if (cookie && api) return "自建服务与登录态均已配置";
  if (cookie) return "已配置登录态";
  if (api) return "已配置自建服务，尚未配置登录态";
  return "当前使用公开接口";
}

function musicCredentialHint(source: string): string {
  const raw = String(settingsForm.value[`${source}_cookie`] ?? "").trim().toLowerCase();
  if (!raw) return "";
  if (source === "qq" && !(raw.includes("uin=") || raw.includes("qqmusic_uin="))) return "当前输入中未发现 uin 或 qqmusic_uin，QQ 会员请求可能无法识别账号。";
  if (source === "qq" && !(raw.includes("qm_keyst=") || raw.includes("qqmusic_key="))) return "当前输入中未发现 qm_keyst 或 qqmusic_key，登录态可能不完整。";
  if (source === "kugou" && !["token=", "userid=", "dfid="].every((key) => raw.includes(key))) return "当前输入中缺少 token、userid 或 dfid，酷狗会员能力可能不可用。";
  return "凭据格式包含所需字段；仍建议点击下方按钮实际测试。";
}

function musicStatusClass(source: string): string {
  const result = musicTestResults.value[source];
  if (!result) return "";
  return result.playable ? "accent" : result.search_ok ? "warn" : "danger";
}

function musicStatusLabel(source: string): string {
  const result = musicTestResults.value[source];
  return result?.message ?? "尚未测试";
}

async function testMusicSettings(): Promise<void> {
  testingMusic.value = true;
  try {
    const response = await testMusicConnections(buildSettingsPayload(), clearSecrets.value);
    musicTestResults.value = Object.fromEntries(response.sources.map((item) => [item.source, item]));
    const playable = response.sources.filter((item) => item.playable).length;
    playable > 0 ? toastSuccess(`${playable} 家曲库可正常取得播放地址`) : toastError("没有曲库能取得播放地址，请按卡片提示检查配置");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "音乐连接测试失败");
  } finally {
    testingMusic.value = false;
  }
}
const repositoryPublishAuthSpec = computed(() => repositoryPublishSpecs.value.find((spec) => spec.key === "github_auth_mode"));
const repositoryPublishTimeoutSpec = computed(() => repositoryPublishSpecs.value.find((spec) => spec.key === "timeout_seconds"));
const issueEnabledRepositories = computed(() => String(repositoryPublishForm.value.allowed_repositories ?? "").split(/[,;；\n\r]/).map((item) => item.trim()).filter(Boolean));
const visibleSettingsSpecs = computed<PluginSettingSpec[]>(() =>
  isGitHubSettings.value ? [] : settingsSpecs.value
);
// 这些键要么由仓库管理和凭据编辑器维护，要么是不该渲染成裸文本框的内部存档。
const repositoryManagedKeys = new Set([
  "github_token", "allowed_repositories", "user_repository_access", "group_repository_access",
  "issue_draft_user_access", "issue_draft_group_access", "issue_manager_user_access", "issue_manager_group_access",
  "user_github_tokens", "user_github_token_users", "user_github_auth_modes",
  "github_credentials", "github_credential_tokens", "github_credential_ids", "repository_credentials",
]);
const githubTokenSpecs = computed<PluginSettingSpec[]>(() => settingsSpecs.value.filter((spec) => spec.key === "github_token"));
// 通知相关的设置按这个顺序排；跟评开关排第一，它是最常被找的那个。
const githubNotifyKeys = ["ask_agent", "follow_up_include_patch", "template_header", "summary_commit_limit"];
const githubGeneralSpecs = computed<PluginSettingSpec[]>(() => settingsSpecs.value.filter((spec) => !repositoryManagedKeys.has(spec.key)));
const githubNotifySpecs = computed<PluginSettingSpec[]>(() =>
  githubNotifyKeys
    .map((key) => githubGeneralSpecs.value.find((spec) => spec.key === key))
    .filter((spec): spec is PluginSettingSpec => Boolean(spec))
);
// 剩下的一律归到「运行」，这样以后新增设置项不会因为漏登记而从界面上消失。
const githubRuntimeSpecs = computed<PluginSettingSpec[]>(() =>
  githubGeneralSpecs.value.filter((spec) => !githubNotifyKeys.includes(spec.key))
);
const activeSettingsForm = computed(() => settingsForm.value);
// 只认已经保存成功的 Token。输入框里刚打的字还没落库，若据此把轮询间隔
// 从 3600 秒改成 60 秒，保存一旦失败就会拿匿名身份高频撞 GitHub 的限额。
const repositoryWatchTokenConfigured = computed(() => {
  const key = "github_token";
  if (clearSecrets.value.includes(key)) return false;
  return secretConfigured(key);
});

function upsert(state: PluginState): void {
  const index = plugins.value.findIndex((plugin) => plugin.manifest.id === state.manifest.id);
  if (index >= 0) {
    plugins.value[index] = state;
  }
}

async function reload(): Promise<void> {
  loading.value = true;
  void loadDependencies();
  try {
    plugins.value = await listPlugins();
    const requestedSettings = viewQuery().get("settings");
    if (!settingsTarget.value && requestedSettings) {
      const target = plugins.value.find((plugin) => plugin.manifest.id === requestedSettings && plugin.installed);
      if (target?.manifest.settings?.length) {
        openSettings(target);
      }
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载插件失败");
  } finally {
    loading.value = false;
  }
}

async function toggleEnabled(plugin: PluginState): Promise<void> {
  busyID.value = plugin.manifest.id;
  const nextEnabled = !pluginEnabled(plugin);
  try {
    upsert(await setPluginEnabled(plugin.manifest.id, nextEnabled));
    if (plugin.manifest.id === repositoryWatchPluginID && repositoryPublishTarget.value?.installed) {
      upsert(await setPluginEnabled(repositoryPublishPluginID, nextEnabled));
    }
    toastSuccess(nextEnabled ? `已启用 ${pluginDisplayName(plugin)}` : `已停用 ${pluginDisplayName(plugin)}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
    await reload();
  } finally {
    busyID.value = "";
  }
}

async function install(plugin: PluginState): Promise<void> {
  busyID.value = plugin.manifest.id;
  try {
    upsert(await installPlugin(plugin.manifest.id));
    toastSuccess(`已安装 ${plugin.manifest.name}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "安装失败");
  } finally {
    busyID.value = "";
  }
}

async function uninstall(plugin: PluginState): Promise<void> {
  const ok = await askConfirm({
    title: "卸载插件",
    message: `确定卸载「${plugin.manifest.name}」吗？插件设置会保留，重新安装后仍然可用。`,
    confirmLabel: "卸载",
    danger: true
  });
  if (!ok) {
    return;
  }
  busyID.value = plugin.manifest.id;
  try {
    upsert(await uninstallPlugin(plugin.manifest.id));
    toastSuccess(`已卸载 ${plugin.manifest.name}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "卸载失败");
  } finally {
    busyID.value = "";
  }
}

function openSettings(plugin: PluginState): void {
  if (plugin.manifest.id === repositoryPublishPluginID) {
    plugin = plugins.value.find((candidate) => candidate.manifest.id === repositoryWatchPluginID) ?? plugin;
  }
  const form: Record<string, any> = {};
  for (const spec of plugin.manifest.settings ?? []) {
    const value = plugin.settings?.[spec.key] ?? spec.default;
    // 数组值拷贝一份，避免勾选直接改到列表里的原对象。
    form[spec.key] = Array.isArray(value) ? [...value] : value;
  }
  settingsForm.value = form;
  const publishForm: Record<string, any> = {};
  const publish = plugins.value.find((candidate) => candidate.manifest.id === repositoryPublishPluginID);
  for (const spec of publish?.manifest.settings ?? []) {
    const value = publish?.settings?.[spec.key] ?? spec.default;
    publishForm[spec.key] = Array.isArray(value) ? [...value] : value;
  }
  repositoryPublishForm.value = publishForm;
  clearSecrets.value = [];
  credentialTokenDrafts.value = {};
  settingsTarget.value = plugin;
  musicTestResults.value = {};
  githubSettingsTab.value = "config";
  openedSnapshot.value = settingsSnapshot();
  if (isGitHubSettings.value) void loadJoinedGroups();
}

async function loadJoinedGroups(): Promise<void> {
  groupsLoading.value = true;
  groupsWarning.value = "";
  try {
    joinedGroups.value = (await listBotGroups()).groups ?? [];
  } catch (error) {
    joinedGroups.value = [];
    groupsWarning.value = error instanceof Error ? error.message : "群列表暂不可用，可手动填写群号";
  } finally {
    groupsLoading.value = false;
  }
}

// 排列方式记在 localStorage：插件多起来之后，横排更利于扫读，
// 但这属于个人偏好，不该每次进页面都重选。
const query = ref("");
const status = ref<"all" | "on" | "off">("all");

const displayPlugins = computed(() => plugins.value.filter((plugin) => plugin.manifest.id !== repositoryPublishPluginID));
const enabledCount = computed(() => displayPlugins.value.filter((plugin) => plugin.installed && pluginEnabled(plugin)).length);

// 搜索同时匹配名称、说明和权限：想找「哪个插件能读消息」时按权限搜得到。
const visiblePlugins = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  return displayPlugins.value.filter((plugin) => {
    const on = plugin.installed && pluginEnabled(plugin);
    if (status.value === "on" && !on) return false;
    if (status.value === "off" && on) return false;
    if (keyword === "") return true;
    const m = plugin.manifest;
    const haystack = [pluginDisplayName(plugin), pluginDisplayDescription(plugin), m.name, m.description, ...(m.permissions ?? [])].join(" ").toLowerCase();
    return haystack.includes(keyword);
  });
});

function pluginEnabled(plugin: PluginState): boolean {
  if (plugin.manifest.id !== repositoryWatchPluginID) return plugin.enabled;
  return plugin.enabled || repositoryPublishTarget.value?.enabled === true;
}

function pluginDisplayName(plugin: PluginState): string {
  return plugin.manifest.id === repositoryWatchPluginID ? "GitHub 仓库" : plugin.manifest.name;
}

function pluginDisplayDescription(plugin: PluginState): string {
  if (plugin.manifest.id !== repositoryWatchPluginID) return plugin.manifest.description;
  return "统一管理 GitHub Token、仓库更新订阅和按仓库的 Issue 能力；草稿与运行记录在设置中的“运行记录”页查看。";
}

type PluginLayout = "tiles" | "rows";
const LAYOUT_KEY = "dqb-next:plugin-layout";
// 只认 "rows"，其余一律当方块：早先存的是 "masonry"，同一个档位换了名字，
// 不值得为它写一次迁移。
const layout = ref<PluginLayout>(
  window.localStorage.getItem(LAYOUT_KEY) === "rows" ? "rows" : "tiles"
);

function setLayout(next: PluginLayout): void {
  layout.value = next;
  window.localStorage.setItem(LAYOUT_KEY, next);
}

// 没有任何可点的动作时不渲染 footer，省掉一整行「无可配置项」。
// 内置插件卸载不了，没有设置项就真的没事可做。
function showFooter(plugin: PluginState): boolean {
  if (!plugin.installed) {
    return true;
  }
  return (plugin.manifest.settings?.length ?? 0) > 0 || !plugin.manifest.built_in;
}

function secretConfigured(key: string): boolean {
  if (isGitHubSettings.value && key === "github_token") {
    return settingsTarget.value?.secrets_configured?.[key] === true || repositoryPublishTarget.value?.secrets_configured?.[key] === true;
  }
  return settingsTarget.value?.secrets_configured?.[key] === true;
}

function secretPlaceholder(key: string): string {
  if (clearSecrets.value.includes(key)) {
    return "保存后将清除";
  }
  return secretConfigured(key) ? "已配置 — 留空沿用，填写则覆盖" : "尚未配置";
}

function toggleClearSecret(key: string): void {
  const index = clearSecrets.value.indexOf(key);
  if (index >= 0) {
    clearSecrets.value.splice(index, 1);
    return;
  }
  clearSecrets.value.push(key);
  activeSettingsForm.value[key] = "";
}

// 打开时的快照，用来判断关闭前有没有未保存的改动。
function settingsSnapshot(): string {
  return JSON.stringify([settingsForm.value, repositoryPublishForm.value]);
}

function settingsDirty(): boolean {
  if (Object.keys(credentialTokenDrafts.value).length > 0) return true;
  if (clearSecrets.value.length > 0) return true;
  // 仓库编辑器的改动不在 settingsForm 里，漏掉它就会从弹窗右上角静默关掉一整屏配置。
  if (repositoryWatchRef.value?.hasUnsavedChanges()) return true;
  if (rssWatchRef.value?.hasUnsavedChanges()) return true;
  return settingsSnapshot() !== openedSnapshot.value;
}

function discardSettings(): void {
  settingsTarget.value = null;
  settingsForm.value = {};
  repositoryPublishForm.value = {};
  credentialTokenDrafts.value = {};
  clearSecrets.value = [];
  openedSnapshot.value = "";
  if (viewQuery().has("settings")) {
    navigate("plugins");
  }
}

async function closeSettings(): Promise<void> {
  // 保存进行中关掉弹窗会让人以为改动没生效，也会把「保存到一半」的状态藏起来。
  if (savingSettings.value) return;
  if (settingsDirty()) {
    const confirmed = await askConfirm({
      title: "放弃未保存的改动？",
      message: "这个弹窗里的改动还没保存，关闭后会丢失。",
      confirmLabel: "放弃改动",
      danger: true,
    });
    if (!confirmed) return;
  }
  discardSettings();
}

async function resetSettings(): Promise<void> {
  // 「恢复默认」会连凭据列表和仓库绑定一起清空，这是不可逆的，必须先问一句。
  const confirmed = await askConfirm({
    title: "恢复默认设置？",
    message: isGitHubSettings.value
      ? "所有设置项会回到默认值，已保存的 Token、凭据列表和仓库绑定都会在保存后被清除。仓库订阅本身不受影响。"
      : "所有设置项会回到默认值，已保存的凭据会在保存后被清除。",
    confirmLabel: "恢复默认",
    danger: true,
  });
  if (!confirmed) return;
  const form: Record<string, any> = {};
  for (const spec of settingsSpecs.value) {
    form[spec.key] = spec.default;
  }
  settingsForm.value = form;
  if (isGitHubSettings.value) {
    const publishForm: Record<string, any> = {};
    for (const spec of repositoryPublishSpecs.value) publishForm[spec.key] = spec.default;
    repositoryPublishForm.value = publishForm;
  }
  // 密钥项置空在后端是「保持不变」的意思，光靠恢复默认清不掉，
  // 必须显式进 clear_secrets，否则这个按钮名不副实。
  credentialTokenDrafts.value = {};
  credentialEditor.value?.clearDrafts();
  const clears = new Set(clearSecrets.value);
  for (const spec of settingsSpecs.value) {
    if (spec.secret && secretConfigured(spec.key)) clears.add(spec.key);
  }
  for (const spec of repositoryPublishSpecs.value) {
    if (spec.secret && repositoryPublishTarget.value?.secrets_configured?.[spec.key]) clears.add(spec.key);
  }
  clearSecrets.value = [...clears];
}

// 只提交与默认值不同的键：等于默认值的键不落库，插件默认值升级后能自动跟随。
function buildSettingsPayload(specs = settingsSpecs.value, form = settingsForm.value): Record<string, unknown> {
  const payload: Record<string, unknown> = {};
  for (const spec of specs) {
    const value = form[spec.key];
    if (spec.type === "number") {
      // 数字输入被清空时视为使用默认值。
      if (value === "" || value === null || Number.isNaN(Number(value))) {
        continue;
      }
      if (Number(value) !== Number(spec.default)) {
        payload[spec.key] = Number(value);
      }
      continue;
    }
    if (spec.type === "multi_select") {
      // 数组按内容比较，与默认勾选一致时不落库。
      const current = Array.isArray(value) ? [...value].sort() : [];
      const defaults = Array.isArray(spec.default) ? [...(spec.default as string[])].sort() : [];
      if (JSON.stringify(current) !== JSON.stringify(defaults)) {
        payload[spec.key] = value;
      }
      continue;
    }
    if (value !== spec.default) {
      payload[spec.key] = value;
    }
  }
  return payload;
}

async function persistSettings(closeAfterSave: boolean): Promise<void> {
  const target = settingsTarget.value;
  if (!target) {
    return;
  }
  savingSettings.value = true;
  try {
    const payload = buildSettingsPayload();
    if (isGitHubSettings.value) {
      // 只提交本次真的输入了的 Token；后端按凭据 ID 合并，没提交的沿用已存值。
      const drafts = credentialTokenDrafts.value;
      payload.github_credential_tokens = Object.keys(drafts).length ? JSON.stringify(drafts) : "";
      // 密钥项不回显，界面靠这份纯 ID 列表判断哪条凭据已经填过 Token。
      const liveIDs = new Set(credentialList.value.map((item) => item.id));
      const configured = new Set(configuredCredentialIDs.value.filter((id) => liveIDs.has(id)));
      for (const id of Object.keys(drafts)) {
        if (liveIDs.has(id)) configured.add(id);
      }
      payload.github_credential_ids = configured.size ? JSON.stringify([...configured]) : "";
    }
    const updated = await updatePluginSettings(target.manifest.id, payload, clearSecrets.value);
    upsert(updated);
    settingsTarget.value = updated;
    if (isGitHubSettings.value && repositoryPublishTarget.value) {
      const publishPayload = buildSettingsPayload(repositoryPublishSpecs.value, repositoryPublishForm.value);
      const sharedToken = String(settingsForm.value.github_token ?? "").trim();
      if (sharedToken) publishPayload.github_token = sharedToken;
      const publishClears = clearSecrets.value.includes("github_token") ? ["github_token"] : [];
      let publishUpdated;
      try {
        publishUpdated = await updatePluginSettings(repositoryPublishPluginID, publishPayload, publishClears);
      } catch (error) {
        // 两次请求没有事务：第一次已经落库了，这里失败会留下半保存状态。
        // 与其只弹一句「保存失败」，不如说清楚哪半边生效了，并把界面刷成真实状态。
        plugins.value = await listPlugins().catch(() => plugins.value);
        const reason = error instanceof Error ? error.message : "未知错误";
        throw new Error(`Token 与仓库检查设置已保存，但 Issue 权限部分没保存成功：${reason}。请重新打开设置检查 Issue 相关配置。`);
      }
      upsert(publishUpdated);
      for (const spec of repositoryPublishSpecs.value) {
        if (spec.secret) repositoryPublishForm.value[spec.key] = "";
      }
    }
    for (const spec of settingsSpecs.value) {
      if (spec.secret) settingsForm.value[spec.key] = "";
    }
    credentialTokenDrafts.value = {};
    credentialEditor.value?.clearDrafts();
    clearSecrets.value = [];
    openedSnapshot.value = settingsSnapshot();
    if (closeAfterSave) {
      toastSuccess(`已保存 ${target.manifest.name} 的设置`);
      discardSettings();
    }
  } finally {
    savingSettings.value = false;
  }
}

async function saveSettings(): Promise<void> {
  try {
    await persistSettings(true);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存设置失败");
  }
}

async function saveSettingsForSubscription(): Promise<void> {
  await persistSettings(false);
}

async function loadDependencies(refresh = false): Promise<void> {
  dependenciesLoading.value = true;
  try {
    const response = await listPluginDependencies(refresh);
    dependencyGroups.value = response.plugins;
  } catch {
    // 依赖探测只是辅助信息，失败不该打断插件页。
    dependencyGroups.value = {};
  } finally {
    dependenciesLoading.value = false;
  }
}

function openDependencies(plugin: PluginState): void {
  dependenciesTarget.value = plugin;
  // 卡片上的比分可能是进页面时探测的，打开时顺手刷新一次。
  void loadDependencies(true);
}

async function refreshDependencies(): Promise<void> {
  await loadDependencies(true);
}

async function installDependency(dependency: ResolverDependency): Promise<void> {
  busyDependency.value = dependency.name;
  try {
    const result = await installResolverDependency(dependency.name);
    // 只合并这次真正受影响的那一组，别把其它插件的探测结果一起覆盖掉。
    dependencyGroups.value = {
      ...dependencyGroups.value,
      ...result.plugins
    };
    toastSuccess(`已安装 ${dependency.name}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : `安装 ${dependency.name} 失败`);
    await loadDependencies(true);
  } finally {
    busyDependency.value = "";
  }
}

onMounted(() => {
  void reload();
});
</script>
