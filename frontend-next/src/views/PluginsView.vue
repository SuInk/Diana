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
            :class="{ active: layout === 'masonry' }"
            title="瀑布流：卡片按高度紧密咬合"
            aria-label="瀑布流排列"
            @click="setLayout('masonry')"
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

    <div v-if="visiblePlugins.length > 0" :class="layout === 'rows' ? 'plugin-rows' : 'plugin-masonry'">
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
              v-if="plugin.manifest.id === resolverPluginID"
              class="plugin-dependencies-head"
              type="button"
              title="查看运行依赖"
              @click="openDependencies"
            >
              <span>运行依赖</span>
              <!-- 缺依赖等于对应平台直接解析失败，这条得能在一屏插件里被一眼扫到 -->
              <span
                v-if="dependencies.length"
                class="plugin-dependency-count"
                :class="{ warn: missingDependencyCount > 0 }"
              >
                {{ readyDependencyCount }}/{{ dependencies.length }}
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
      :wide="settingsTarget.manifest.id === repositoryWatchPluginID || settingsTarget.manifest.id === repositoryPublishPluginID || settingsTarget.manifest.id === rssWatchPluginID"
      @close="closeSettings"
    >
      <!-- 依赖多数时候是齐的，默认折叠把弹窗顶部让给真正要改的设置项；
           缺依赖时自动展开，那才是需要立刻处理的状态。 -->
      <details
        v-if="settingsTarget.manifest.id === resolverPluginID"
        class="plugin-settings-section-head plugin-settings-collapsible"
        :open="missingDependencyCount > 0"
      >
        <summary>
          <h3>运行依赖</h3>
          <span v-if="dependencies.length" class="badge" :class="missingDependencyCount > 0 ? 'warn' : 'accent'">
            {{ readyDependencyCount }}/{{ dependencies.length }}
          </span>
          <ChevronDown class="plugin-settings-chevron" :size="15" aria-hidden="true" />
        </summary>
        <p>缺少这些命令时，对应平台的解析会失败；可直接在这里安装。</p>
        <PluginDependencyList
          :dependencies="dependencies"
          :loading="dependenciesLoading"
          :busy="busyDependency"
          @install="installDependency"
        />
      </details>

      <div v-if="isGitHubSettings" class="segmented github-settings-tabs" role="tablist" aria-label="GitHub 仓库设置">
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'token'" :class="{ active: githubSettingsTab === 'token' }" @click="githubSettingsTab = 'token'">Token</button>
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'repositories'" :class="{ active: githubSettingsTab === 'repositories' }" @click="githubSettingsTab = 'repositories'">仓库管理</button>
        <button type="button" role="tab" :aria-selected="githubSettingsTab === 'records'" :class="{ active: githubSettingsTab === 'records' }" @click="githubSettingsTab = 'records'">运行记录</button>
      </div>

      <div v-if="isGitHubSettings && githubSettingsTab === 'token'" class="plugin-settings-section-head">
        <h3>GitHub 认证</h3>
        <p>公共 Token 同时用于仓库更新检查和 Issue 创建；具体仓库是否允许 Issue 操作，在“仓库管理”中配置。</p>
        <div class="token-type-guide">
          <p><strong>Fine-grained token</strong>：只授权选定的仓库和具体权限，适合只管理自己明确配置的仓库，权限更小、更安全。</p>
          <p><strong>Classic token</strong>：权限范围更大；需要查看未加入白名单的仓库、跨账号或组织访问，或调用更多旧版 GitHub API 能力时使用。</p>
          <p>公开仓库也可以匿名读取，但请求额度较低；配置 Token 后可提高额度。无论哪种 Token，都必须拥有目标仓库本身的访问权限。<a class="token-create-link" href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noreferrer"><ExternalLink :size="13" aria-hidden="true" />创建 Token</a></p>
        </div>
        <button class="btn small ghost github-settings-link" type="button" @click="githubSettingsTab = 'repositories'">
          去仓库管理配置用户和群聊
          <ArrowRight :size="14" aria-hidden="true" />
        </button>
      </div>
      <div v-if="isGitHubSettings && githubSettingsTab === 'token'" class="stack plugin-settings-form github-publish-global-settings">
        <div v-if="repositoryPublishAuthSpec" class="field">
          <label for="setting-github_auth_mode">{{ repositoryPublishAuthSpec.label }}</label>
          <AppSelect id="setting-github_auth_mode" v-model="repositoryPublishForm.github_auth_mode" :options="repositoryPublishAuthSpec.options ?? []" />
          <span v-if="repositoryPublishAuthSpec.description" class="hint">{{ repositoryPublishAuthSpec.description }}</span>
        </div>
        <div v-if="repositoryPublishTimeoutSpec" class="field">
          <label for="setting-publish-timeout">{{ repositoryPublishTimeoutSpec.label }}</label>
          <div class="plugin-setting-number">
            <input id="setting-publish-timeout" v-model.number="repositoryPublishForm.timeout_seconds" class="input" type="number" :min="repositoryPublishTimeoutSpec.min" :max="repositoryPublishTimeoutSpec.max" :step="repositoryPublishTimeoutSpec.step || 1" />
            <span v-if="repositoryPublishTimeoutSpec.unit" class="plugin-setting-unit">{{ repositoryPublishTimeoutSpec.unit }}</span>
          </div>
          <span v-if="repositoryPublishTimeoutSpec.description" class="hint">{{ repositoryPublishTimeoutSpec.description }}</span>
        </div>
      </div>
      <div v-if="!isGitHubSettings || githubSettingsTab !== 'records'" class="stack plugin-settings-form">
        <div v-for="spec in visibleSettingsSpecs" :key="spec.key" class="field">
          <template v-if="spec.type === 'bool'">
            <div class="plugin-setting-switch">
              <div class="plugin-setting-switch-text">
                <label :for="`setting-${spec.key}`">{{ spec.label }}</label>
                <span v-if="spec.description" class="hint">{{ spec.description }}</span>
              </div>
              <label class="switch">
                <input :id="`setting-${spec.key}`" v-model="activeSettingsForm[spec.key]" type="checkbox" />
                <span class="track" aria-hidden="true"></span>
              </label>
            </div>
          </template>
          <template v-else>
            <label :for="`setting-${spec.key}`">{{ spec.label }}</label>
            <div v-if="spec.type === 'number'" class="plugin-setting-number">
              <input
                :id="`setting-${spec.key}`"
                v-model.number="activeSettingsForm[spec.key]"
                class="input"
                type="number"
                :min="spec.min"
                :max="spec.max"
                :step="spec.step || 1"
              />
              <span v-if="spec.unit" class="plugin-setting-unit">{{ spec.unit }}</span>
            </div>
            <AppSelect
              v-else-if="spec.type === 'select'"
              :id="`setting-${spec.key}`"
              v-model="activeSettingsForm[spec.key]"
              :options="spec.options ?? []"
            />
            <div v-else-if="spec.type === 'multi_select'" class="plugin-setting-checks">
              <label v-for="option in spec.options ?? []" :key="option.value" class="check-item">
                <input
                  type="checkbox"
                  :checked="multiSelected(spec.key, option.value)"
                  @change="toggleMultiSelect(spec.key, option.value, $event)"
                />
                <span>{{ option.label }}</span>
              </label>
            </div>
            <div v-else-if="spec.secret" class="input-group">
              <input
                :id="`setting-${spec.key}`"
                v-model="activeSettingsForm[spec.key]"
                class="input"
                type="password"
                autocomplete="off"
                :disabled="clearSecrets.includes(spec.key)"
                :placeholder="secretPlaceholder(spec.key)"
              />
              <button
                v-if="secretConfigured(spec.key)"
                class="btn small"
                type="button"
                @click="toggleClearSecret(spec.key)"
              >
                {{ clearSecrets.includes(spec.key) ? "取消清除" : "清除" }}
              </button>
            </div>
            <input v-else :id="`setting-${spec.key}`" v-model="activeSettingsForm[spec.key]" class="input" type="text" />
            <span v-if="spec.description" class="hint">{{ spec.description }}</span>
          </template>
        </div>
      </div>
      <RepositoryWatchManager
        v-if="isGitHubSettings && githubSettingsTab === 'repositories'"
        :prepare-access="saveSettingsForSubscription"
        :token-configured="repositoryWatchTokenConfigured"
        :issue-enabled-repositories="issueEnabledRepositories"
        :user-access="String(repositoryPublishForm.user_repository_access ?? '')"
        :group-access="String(repositoryPublishForm.group_repository_access ?? '')"
        :draft-user-access="String(repositoryPublishForm.issue_draft_user_access ?? '')"
        :draft-group-access="String(repositoryPublishForm.issue_draft_group_access ?? '')"
        :manager-user-access="String(repositoryPublishForm.issue_manager_user_access ?? '')"
        :manager-group-access="String(repositoryPublishForm.issue_manager_group_access ?? '')"
        :token-users="String(repositoryPublishForm.user_github_token_users ?? '')"
        :user-auth-modes="String(repositoryPublishForm.user_github_auth_modes ?? '')"
        :joined-groups="joinedGroups"
        :groups-loading="groupsLoading"
        :groups-warning="groupsWarning"
        @update:issue-enabled-repositories="repositoryPublishForm.allowed_repositories = $event.join('\n')"
        @update:user-access="repositoryPublishForm.user_repository_access = $event"
        @update:group-access="repositoryPublishForm.group_repository_access = $event"
        @update:draft-user-access="repositoryPublishForm.issue_draft_user_access = $event"
        @update:draft-group-access="repositoryPublishForm.issue_draft_group_access = $event"
        @update:manager-user-access="repositoryPublishForm.issue_manager_user_access = $event"
        @update:manager-group-access="repositoryPublishForm.issue_manager_group_access = $event"
        @update:user-tokens="repositoryPublishForm.user_github_tokens = $event"
        @update:token-users="repositoryPublishForm.user_github_token_users = $event"
        @update:user-auth-modes="repositoryPublishForm.user_github_auth_modes = $event"
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

    <Modal v-if="dependenciesOpen" title="运行依赖" @close="dependenciesOpen = false">
      <p class="plugin-dependencies-hint">缺少这些命令时，对应平台的解析会失败；可直接在这里安装。</p>
      <PluginDependencyList
        :dependencies="dependencies"
        :loading="dependenciesLoading"
        :busy="busyDependency"
        @install="installDependency"
      />
      <template #footer>
        <button class="btn" type="button" @click="refreshDependencies">重新检测</button>
        <button class="btn primary" type="button" @click="dependenciesOpen = false">完成</button>
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
  listResolverDependencies,
  listQQBotGroups,
  type PluginSettingSpec,
  type PluginState,
  type ResolverDependency,
  type QQBotGroupSummary
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import AppSelect from "../components/AppSelect.vue";
import Modal from "../components/Modal.vue";
import RepositoryIssueDraftList from "../components/RepositoryIssueDraftList.vue";
import RepositoryWatchManager from "../components/RepositoryWatchManager.vue";
import RSSWatchManager from "../components/RSSWatchManager.vue";
import PluginDependencyList from "../components/PluginDependencyList.vue";
import { navigate, viewQuery } from "../router";

const plugins = ref<PluginState[]>([]);
const loading = ref(false);
const busyID = ref("");

const resolverPluginID = "official.nonebot-plugin-resolver-go";
const repositoryWatchPluginID = "official.repository-watch";
const repositoryPublishPluginID = "official.repository-publish";
const rssWatchPluginID = "official.rss-watch";
const dependencies = ref<ResolverDependency[]>([]);
const dependenciesLoading = ref(false);
const busyDependency = ref("");
const dependenciesOpen = ref(false);
const permissionsTarget = ref<PluginState | null>(null);
const readyDependencyCount = computed(() => dependencies.value.filter((dep) => dep.available).length);
const missingDependencyCount = computed(() => dependencies.value.length - readyDependencyCount.value);

const settingsTarget = ref<PluginState | null>(null);
// 表单值按 spec.type 渲染成对应控件，这里用宽松类型换取模板里干净的 v-model 绑定。
const settingsForm = ref<Record<string, any>>({});
const repositoryPublishForm = ref<Record<string, any>>({});
const clearSecrets = ref<string[]>([]);
const savingSettings = ref(false);
const githubSettingsTab = ref<"token" | "repositories" | "records">("token");
const joinedGroups = ref<QQBotGroupSummary[]>([]);
const groupsLoading = ref(false);
const groupsWarning = ref("");

const settingsSpecs = computed<PluginSettingSpec[]>(() => settingsTarget.value?.manifest.settings ?? []);
const repositoryPublishTarget = computed(() => plugins.value.find((plugin) => plugin.manifest.id === repositoryPublishPluginID) ?? null);
const repositoryPublishSpecs = computed<PluginSettingSpec[]>(() => repositoryPublishTarget.value?.manifest.settings ?? []);
const isGitHubSettings = computed(() => settingsTarget.value?.manifest.id === repositoryWatchPluginID);
const repositoryPublishAuthSpec = computed(() => repositoryPublishSpecs.value.find((spec) => spec.key === "github_auth_mode"));
const repositoryPublishTimeoutSpec = computed(() => repositoryPublishSpecs.value.find((spec) => spec.key === "timeout_seconds"));
const issueEnabledRepositories = computed(() => String(repositoryPublishForm.value.allowed_repositories ?? "").split(/[,;；\n\r]/).map((item) => item.trim()).filter(Boolean));
const visibleSettingsSpecs = computed<PluginSettingSpec[]>(() => {
  if (!isGitHubSettings.value) return settingsSpecs.value;
  if (githubSettingsTab.value === "token") return settingsSpecs.value.filter((spec) => spec.key === "github_token");
  if (githubSettingsTab.value === "records") return [];
  const repositoryManagedKeys = new Set(["github_token", "allowed_repositories", "user_repository_access", "group_repository_access", "issue_draft_user_access", "issue_draft_group_access", "issue_manager_user_access", "issue_manager_group_access", "user_github_tokens", "user_github_token_users", "user_github_auth_modes"]);
  return settingsSpecs.value.filter((spec) => !repositoryManagedKeys.has(spec.key));
});
const activeSettingsForm = computed(() => settingsForm.value);
const repositoryWatchTokenConfigured = computed(() => {
  const key = "github_token";
  if (clearSecrets.value.includes(key)) return false;
  return String(settingsForm.value[key] ?? "").trim() !== "" || secretConfigured(key);
});

function upsert(state: PluginState): void {
  const index = plugins.value.findIndex((plugin) => plugin.manifest.id === state.manifest.id);
  if (index >= 0) {
    plugins.value[index] = state;
  }
}

async function reload(): Promise<void> {
  loading.value = true;
  const dependencyRequest = loadDependencies();
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
    await dependencyRequest;
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
  settingsTarget.value = plugin;
  githubSettingsTab.value = "token";
  if (isGitHubSettings.value) void loadJoinedGroups();
}

async function loadJoinedGroups(): Promise<void> {
  groupsLoading.value = true;
  groupsWarning.value = "";
  try {
    joinedGroups.value = (await listQQBotGroups()).groups ?? [];
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

type PluginLayout = "masonry" | "rows";
const LAYOUT_KEY = "dqb-next:plugin-layout";
const layout = ref<PluginLayout>(
  window.localStorage.getItem(LAYOUT_KEY) === "rows" ? "rows" : "masonry"
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

function multiSelected(key: string, option: string): boolean {
  const value = activeSettingsForm.value[key];
  return Array.isArray(value) && value.includes(option);
}

function toggleMultiSelect(key: string, option: string, event: Event): void {
  const checked = (event.target as HTMLInputElement).checked;
  const current: string[] = Array.isArray(activeSettingsForm.value[key]) ? [...activeSettingsForm.value[key]] : [];
  if (checked && !current.includes(option)) {
    current.push(option);
  }
  if (!checked) {
    const index = current.indexOf(option);
    if (index >= 0) {
      current.splice(index, 1);
    }
  }
  activeSettingsForm.value[key] = current;
}

function closeSettings(): void {
  settingsTarget.value = null;
  settingsForm.value = {};
  repositoryPublishForm.value = {};
  clearSecrets.value = [];
  if (viewQuery().has("settings")) {
    navigate("plugins");
  }
}

function resetSettings(): void {
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
    const updated = await updatePluginSettings(target.manifest.id, buildSettingsPayload(), clearSecrets.value);
    upsert(updated);
    settingsTarget.value = updated;
    if (isGitHubSettings.value && repositoryPublishTarget.value) {
      const publishPayload = buildSettingsPayload(repositoryPublishSpecs.value, repositoryPublishForm.value);
      const sharedToken = String(settingsForm.value.github_token ?? "").trim();
      if (sharedToken) publishPayload.github_token = sharedToken;
      const publishClears = clearSecrets.value.includes("github_token") ? ["github_token"] : [];
      const publishUpdated = await updatePluginSettings(repositoryPublishPluginID, publishPayload, publishClears);
      upsert(publishUpdated);
      for (const spec of repositoryPublishSpecs.value) {
        if (spec.secret) repositoryPublishForm.value[spec.key] = "";
      }
    }
    for (const spec of settingsSpecs.value) {
      if (spec.secret) settingsForm.value[spec.key] = "";
    }
    clearSecrets.value = [];
    if (closeAfterSave) {
      toastSuccess(`已保存 ${target.manifest.name} 的设置`);
      closeSettings();
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

async function loadDependencies(): Promise<void> {
  dependenciesLoading.value = true;
  try {
    dependencies.value = (await listResolverDependencies()).resolver;
  } catch {
    // 依赖探测只是辅助信息，失败不该打断插件页。
    dependencies.value = [];
  } finally {
    dependenciesLoading.value = false;
  }
}

function openDependencies(): void {
  dependenciesOpen.value = true;
  // 卡片上的比分可能是进页面时探测的，打开时顺手刷新一次。
  void loadDependencies();
}

async function refreshDependencies(): Promise<void> {
  await loadDependencies();
}

async function installDependency(dependency: ResolverDependency): Promise<void> {
  busyDependency.value = dependency.name;
  try {
    const result = await installResolverDependency(dependency.name);
    dependencies.value = result.resolver;
    toastSuccess(`已安装 ${dependency.name}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : `安装 ${dependency.name} 失败`);
    await loadDependencies();
  } finally {
    busyDependency.value = "";
  }
}

onMounted(() => {
  void reload();
});
</script>
