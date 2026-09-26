<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="extensions-view">
    <div class="segmented extension-tabs" role="tablist" aria-label="扩展类型">
      <button v-for="tab in extensionTabs" :key="tab.value" type="button" role="tab" :aria-selected="extensionTab === tab.value" :class="{active:extensionTab === tab.value}" @click="changeExtensionTab(tab.value)">{{ tab.label }}</button>
    </div>
    <ExtensionManager v-if="extensionTab === 'skill' || extensionTab === 'mcp'" ref="extensionManager" :key="extensionTab" :kind="extensionTab" />
  <div v-show="extensionTab === 'plugins'" class="plugins-view">
    <header class="view-header plugins-view-header">
      <div class="view-title">
        <p>{{ botScope ? "插件开关按机器人独立，配置全局共享" : "共享插件配置" }} · OpenAPI 位于系统设置</p>
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
            <span class="plugin-filter-count"><SkeletonBlock v-if="loading && !plugins.length" width="2ch" inline /><template v-else>{{ displayPlugins.length }}</template></span>
          </button>
          <button type="button" role="radio" :aria-checked="status === 'on'" :class="{ active: status === 'on' }" @click="status = 'on'">
            <span>已启用</span>
            <span class="plugin-filter-count"><SkeletonBlock v-if="loading && !plugins.length" width="2ch" inline /><template v-else>{{ enabledCount }}</template></span>
          </button>
          <button type="button" role="radio" :aria-checked="status === 'off'" :class="{ active: status === 'off' }" @click="status = 'off'">
            <span>已停用</span>
            <span class="plugin-filter-count"><SkeletonBlock v-if="loading && !plugins.length" width="2ch" inline /><template v-else>{{ displayPlugins.length - enabledCount }}</template></span>
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
        <button class="btn primary" type="button" @click="openRepoInstall()">
          <Download :size="15" aria-hidden="true" />
          安装插件
        </button>
      </div>
    </header>

    <div v-if="loadError" class="plugin-load-error" role="alert">
      <span>{{ loadError }}</span>
      <button class="btn" type="button" @click="reload"><RefreshCw :size="15" />重试</button>
    </div>
    <LoadingSkeleton v-if="loading && !plugins.length" kind="plugins" :layout="layout" :count="8" label="正在加载插件" />
    <div v-else-if="visiblePlugins.length > 0" :class="layout === 'rows' ? 'plugin-rows' : 'plugin-tiles'" :aria-busy="loading || undefined">
      <article
        v-for="plugin in visiblePlugins"
        :key="plugin.manifest.id"
        class="plugin-card"
        :class="{ off: plugin.installed && !pluginEnabled(plugin), uninstalled: !plugin.installed }"
      >
        <div class="plugin-card-head">
          <h2 class="plugin-card-name">{{ pluginDisplayName(plugin) }}</h2>
          <label
            v-if="plugin.installed && botScope"
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
          <span
            v-for="platform in pluginPlatformBadges(plugin)"
            :key="platform.id"
            class="badge"
            :title="platform.note || `支持 ${platform.label}`"
          >{{ platform.label }}</span>
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
              <!-- 更新与安装同一套确认流程：重新预览权限、重新勾风险，不静默升级 -->
              <button
                v-if="plugin.repo_source"
                class="btn small"
                type="button"
                :disabled="busyID === plugin.manifest.id"
                @click="openRepoInstall(repoInstallLink(plugin))"
              >
                <RefreshCw :size="14" aria-hidden="true" />
                更新
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
    <EmptyState v-else-if="!loading && !loadError" :title="botScope ? '没有可用插件' : '请在左侧选择具体机器人'" />

    <Modal
      v-if="settingsTarget"
      :title="isGitHubSettings ? 'GitHub 仓库 · 设置' : `${settingsTarget.manifest.name} · 设置`"
      :wide="settingsTarget.manifest.id === repositoryWatchPluginID || settingsTarget.manifest.id === repositoryPublishPluginID || settingsTarget.manifest.id === rssWatchPluginID || settingsTarget.manifest.id === musicPluginID || settingsTarget.manifest.id === stickerPluginID"
      @close="closeSettings"
    >
      <p class="hint">设置和凭据全局共享；保存或恢复默认会影响使用此插件的所有机器人。</p>
      <!-- Dependencies expand only when attention is needed. -->
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

      <p class="hint">所有机器人共用此插件配置，启用状态各自独立。</p>
      <p v-if="settingsTarget.shared_config_source" class="hint">当前共享配置迁移自机器人 {{ settingsTarget.shared_config_source }}，旧的独立配置仍保留在存储中。</p>
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
            <p v-if="musicTestResults[platform.key]?.login" class="credential-check-line">
              <span class="badge" :class="credentialBadgeClass(musicTestResults[platform.key].login!.state)">{{ credentialStateLabel(musicTestResults[platform.key].login!) }}</span>
              {{ musicTestResults[platform.key].login!.message }}
            </p>
          </section>
        </div>
        <button class="btn music-test-button" type="button" :disabled="testingMusic" @click="testMusicSettings">
          <RefreshCw :size="15" :class="{ spin: testingMusic }" aria-hidden="true" />
          {{ testingMusic ? "正在测试三家曲库" : "测试登录、连接与播放能力" }}
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
        <template v-for="spec in visibleSettingsSpecs" :key="spec.key">
          <PlatformLevelRulesField
            v-if="spec.type === 'platform_level_rules'"
            :spec="spec"
            :model-value="settingsForm[spec.key]"
            @update:model-value="settingsForm[spec.key] = $event"
          />
          <PluginSettingField
            v-else
            :spec="spec"
            :form="settingsForm"
            :clearing="clearSecrets.includes(spec.key)"
            :secret-configured="secretConfigured(spec.key)"
            :secret-placeholder="secretPlaceholder(spec.key)"
            @toggle-clear="toggleClearSecret"
          />
        </template>
      </div>
      <section v-if="isResolverSettings" class="credential-check">
        <div class="credential-check-head">
          <div>
            <h4>登录凭据自检</h4>
            <span class="hint">用各平台的账号接口实测，框里还没保存的输入也会一起测。</span>
          </div>
          <button class="btn small" type="button" :disabled="testingResolver" @click="testResolverSettings">
            <RefreshCw :size="14" :class="{ spin: testingResolver }" aria-hidden="true" />
            {{ testingResolver ? "正在测试" : "测试登录凭据" }}
          </button>
        </div>
        <ul v-if="resolverChecks.length" class="credential-check-list">
          <li v-for="check in resolverChecks" :key="check.key">
            <div class="credential-check-row">
              <strong>{{ check.label }}</strong>
              <span class="badge" :class="credentialBadgeClass(check.state)">{{ credentialStateLabel(check) }}</span>
            </div>
            <p v-if="check.state !== 'unconfigured'">{{ check.message }}</p>
          </li>
        </ul>
      </section>
      <RepositoryWatchManager
        v-if="isGitHubSettings && githubSettingsTab === 'repositories'"
        :default-profile-id="botScope"
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
      <StickerLibrary v-if="settingsTarget.manifest.id === stickerPluginID" :profile="botScope" />
      <VRChatStatusPanel v-if="settingsTarget.manifest.id === vrchatPluginID" />
      <RSSWatchManager
        v-if="settingsTarget.manifest.id === rssWatchPluginID"
        :default-profile-id="botScope"
        ref="rssWatchRef"
        :prepare-access="saveSettingsForSubscription"
      />
      <template #footer>
        <button class="btn ghost small plugin-settings-reset" type="button" :disabled="settingsBusy" @click="resetSettings">
          恢复默认
        </button>
        <button class="btn" type="button" :disabled="settingsBusy" @click="closeSettings">取消</button>
        <button class="btn primary" type="button" :disabled="settingsBusy" @click="saveSettings">保存</button>
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

    <Modal v-if="repoInstallOpen" title="从 GitHub 安装插件" @close="closeRepoInstall">
      <div class="repo-install">
        <div class="field">
          <label for="repo-plugin-url">GitHub 仓库链接</label>
          <input
            id="repo-plugin-url"
            v-model="repoInstallURL"
            class="input"
            type="url"
            placeholder="github.com/作者/仓库，或 …/tree/v1.0.0 固定版本"
            :disabled="repoCheckBusy || !!repoPreview"
            @keydown.enter="checkRepoPlugin"
          />
          <p class="hint">默认安装默认分支最新提交；用 /tree/版本tag 的链接可以固定版本、复现安装。</p>
        </div>
        <p v-if="repoInstallError" class="repo-install-error" role="alert">{{ repoInstallError }}</p>
        <div v-if="repoPreview" class="repo-preview">
          <div class="repo-preview-head">
            <strong>{{ repoPreview.manifest.name }}</strong>
            <span class="badge mono">v{{ repoPreview.manifest.version }}</span>
            <span class="badge warn">第三方</span>
          </div>
          <p class="repo-preview-desc">{{ repoPreview.manifest.description }}</p>
          <p class="repo-preview-source">
            <a :href="repoPreviewLink" target="_blank" rel="noreferrer">{{ repoPreviewLink }}</a>
            <span class="hint mono">提交 {{ repoPreview.commit.slice(0, 12) }}</span>
          </p>
          <section class="repo-preview-section">
            <h3>作者声明的权限（{{ repoPreview.permissions.length }} 项）</h3>
            <p class="hint">这是插件作者的声明，Diana 目前不据此限制插件，见下方风险说明。</p>
            <ul class="repo-permission-list">
              <li v-for="permission in repoPreview.permissions" :key="permission.id" :class="{ sensitive: permission.sensitive }">
                <span>{{ permission.label }}</span>
                <span v-if="permission.sensitive" class="badge warn">高敏感</span>
                <code>{{ permission.id }}</code>
              </li>
            </ul>
          </section>
          <section v-if="repoPreview.manifest.settings?.length" class="repo-preview-section">
            <h3>设置项（{{ repoPreview.manifest.settings.length }} 项）</h3>
            <ul class="repo-setting-list">
              <li v-for="spec in repoPreview.manifest.settings" :key="spec.key">
                <span>{{ spec.label }}</span>
                <span v-if="spec.secret" class="badge warn">凭据</span>
              </li>
            </ul>
            <p class="hint">凭据类设置安装后请到插件设置里配置；读接口不回传明文。</p>
          </section>
          <p v-if="repoPreview.installed" class="repo-install-error" role="alert">
            <template v-if="repoPreview.installed.built_in">
              这个 ID 属于内置插件，不能被第三方插件替换。
            </template>
            <template v-else>
              已安装同 ID 插件 v{{ repoPreview.installed.version }}，这次是
              v{{ repoPreview.manifest.version }}（{{ repoReplaceLabel }}）。覆盖会连带接管它已配置的设置与凭据。
            </template>
          </p>
          <label v-if="repoPreview.installed && !repoPreview.installed.built_in" class="repo-risk-ack">
            <input v-model="repoReplaceAccepted" type="checkbox" />
            <span>我确认要覆盖已安装的版本</span>
          </label>
          <ul class="repo-risk-list">
            <li v-for="(warning, index) in repoPreview.risk.warnings" :key="index">{{ warning }}</li>
          </ul>
          <!-- 默认不勾选：安装第三方插件是显式信任动作，不能替用户做决定 -->
          <label class="repo-risk-ack">
            <input v-model="repoRiskAccepted" type="checkbox" />
            <span>我已了解上述风险，确认安装此第三方插件</span>
          </label>
        </div>
      </div>
      <template #footer>
        <template v-if="repoPreview">
          <button class="btn" type="button" :disabled="repoInstallBusy" @click="resetRepoPreview">返回</button>
          <button class="btn primary" type="button" :disabled="!repoRiskAccepted || repoInstallBlocked || repoInstallBusy" @click="confirmRepoInstall">
            {{ repoInstallBusy ? "安装中…" : "确认安装" }}
          </button>
        </template>
        <template v-else>
          <button class="btn" type="button" :disabled="repoCheckBusy" @click="closeRepoInstall">取消</button>
          <button class="btn primary" type="button" :disabled="repoCheckBusy || !repoInstallURL.trim()" @click="checkRepoPlugin">
            {{ repoCheckBusy ? "检查中…" : "检查" }}
          </button>
        </template>
      </template>
    </Modal>
  </div>
  </div>
</template>

<style scoped>
.repo-install-error {
  margin: 0 0 12px;
  padding: 8px 12px;
  border-radius: 8px;
  background: var(--danger-soft, rgba(220, 38, 38, 0.08));
  color: var(--danger, #dc2626);
  font-size: 13px;
}

.repo-preview {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 4px;
}

.repo-preview-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
}

.repo-preview-desc {
  margin: 0;
  color: var(--text-secondary, #666);
  font-size: 13px;
}

.repo-preview-source {
  margin: 0;
  font-size: 12px;
  word-break: break-all;
}

.repo-preview-section h3 {
  margin: 0 0 6px;
  font-size: 13px;
  font-weight: 600;
}

.repo-permission-list,
.repo-setting-list,
.repo-risk-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
}

.repo-permission-list li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 8px;
  background: var(--bg-secondary, rgba(127, 127, 127, 0.08));
}

.repo-permission-list li.sensitive {
  outline: 1px solid var(--danger, #dc2626);
}

.repo-permission-list code {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-secondary, #888);
}

.repo-setting-list li {
  display: flex;
  align-items: center;
  gap: 8px;
}

.repo-risk-list {
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--warning-soft, rgba(217, 119, 6, 0.1));
  color: var(--text-secondary, #666);
}

.repo-risk-list li + li {
  margin-top: 4px;
}

.repo-risk-ack {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
}
</style>

<script setup lang="ts">
import { useConfigurationRefresh } from "../configuration-sync";
import { computed, onMounted, ref, watch } from "vue";
import ExtensionManager from "../components/ExtensionManager.vue";
const extensionTabs = [{value:'plugins' as const,label:'插件'},{value:'skill' as const,label:'Skills'},{value:'mcp' as const,label:'MCP'}];
type ExtensionTab = typeof extensionTabs[number]['value'];
const extensionTab = ref<ExtensionTab>('plugins');
const extensionManager = ref<InstanceType<typeof ExtensionManager> | null>(null);
async function changeExtensionTab(value:ExtensionTab) {
  if (value === extensionTab.value) return;
  if (extensionManager.value && !await extensionManager.value.prepareLeave()) return;
  if (settingsTarget.value) { await closeSettings(); if (settingsTarget.value) return; }
  extensionTab.value=value;
}
import { ArrowRight, ChevronDown, Download, ExternalLink, LayoutGrid, RefreshCw, Rows3, Search, SlidersHorizontal } from "@lucide/vue";
import {
  installPlugin,
  installRepoPlugin,
  installResolverDependency,
  listPlugins,
  previewRepoPlugin,
  setPluginEnabled,
  uninstallPlugin,
  updatePluginSettings,
  testMusicConnections,
  testResolverCredentials,
  listPluginDependencies,
  listBotGroups,
  type PluginSettingSpec,
  type PluginState,
  type RepoPluginPreview,
  type ResolverDependency,
  type BotGroupSummary,
  type MusicConnectionStatus,
  type CredentialCheck,
  type CredentialState
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import AppSelect from "../components/AppSelect.vue";
import PluginSettingField from "../components/PluginSettingField.vue";
import PlatformLevelRulesField from "../components/PlatformLevelRulesField.vue";
import Modal from "../components/Modal.vue";
import RepositoryIssueDraftList from "../components/RepositoryIssueDraftList.vue";
import RepositoryCredentialEditor from "../components/RepositoryCredentialEditor.vue";
import RepositoryWatchManager from "../components/RepositoryWatchManager.vue";
import StickerLibrary from "../components/StickerLibrary.vue";
import VRChatStatusPanel from "../components/VRChatStatusPanel.vue";
import RSSWatchManager from "../components/RSSWatchManager.vue";
import PluginDependencyList from "../components/PluginDependencyList.vue";
import { navigate, viewQuery } from "../router";
import { botScope } from "../bot-scope";
import { extensionLayout, setExtensionLayout } from "../extension-layout";
import { pluginForBot } from "../plugin-settings";

const plugins = ref<PluginState[]>([]);
const loading = ref(true);
const loadError = ref("");
const busyID = ref("");

const resolverPluginID = "official.nonebot-plugin-resolver-go";
const sandboxedBrowserPluginID = "official.sandboxed-browser-renderer";
const groupRelationsPluginID = "group_relations";
const repositoryWatchPluginID = "official.repository-watch";
const repositoryPublishPluginID = "official.repository-publish";
const rssWatchPluginID = "official.rss-watch";
const musicPluginID = "official.music";
const stickerPluginID = "official.sticker-sender";
const vrchatPluginID = "official.vrchat-osc";
// 依赖按插件 ID 分组：链接解析要 yt-dlp/ffmpeg，网页渲染要一个
// Chrome/Chromium，以后再有别的插件也不必再往模板里加一个 id 判断。
const dependencyGroups = ref<Record<string, ResolverDependency[]>>({});
const dependenciesLoading = ref(false);
const busyDependency = ref("");
const dependenciesTarget = ref<PluginState | null>(null);
const permissionsTarget = ref<PluginState | null>(null);

const dependencyHints: Record<string, string> = {
  [resolverPluginID]: "缺少这些命令时，对应平台的解析会失败；可直接在这里安装。",
  [sandboxedBrowserPluginID]:
    "复用系统 Chromium / Google Chrome；缺少时通过系统包管理器安装（Linux 安装 Chromium，macOS / Windows 安装 Chrome），安装需要相应系统权限。中文字体可一键下载；出图时按实际文字补齐缺失的多语言字体和单色 Emoji 字体，保存到应用缓存，无需管理员权限。官方 Docker 镜像已预装浏览器和中文字体。",
  [groupRelationsPluginID]:
    "关系图优先使用中文字体直接出图；缺失时可一键下载，首次出图也会自动补齐。浏览器截图作为备用路径。"
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
  if (pluginID === groupRelationsPluginID) return !dependenciesFor(pluginID).some((dep) => dep.name === "cjk-font" && dep.available);
  return missingDependencyCount(pluginID) > 0;
}

function dependencyHint(pluginID: string): string {
  return dependencyHints[pluginID] ?? "缺少这些依赖时，这个插件不会正常工作。";
}

const settingsTarget = ref<PluginState | null>(null);
type SubscriptionEditorHandle = { hasUnsavedChanges: () => boolean; saveEditor: () => Promise<boolean> };
const repositoryWatchRef = ref<SubscriptionEditorHandle | null>(null);
const rssWatchRef = ref<SubscriptionEditorHandle | null>(null);
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
// 订阅编辑器的提交分两步：先经 prepareAccess 存插件设置，再存订阅。savingSettings 只罩住第一步，
// 第二步期间也得锁住底部按钮，否则能重复点保存或中途关掉弹窗。
const savingSubscription = ref(false);
const settingsBusy = computed(() => savingSettings.value || savingSubscription.value);
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
  if (result.login?.state === "invalid") return "err";
  return result.playable ? "accent" : result.search_ok ? "warn" : "err";
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

const isResolverSettings = computed(() => settingsTarget.value?.manifest.id === resolverPluginID);
const testingResolver = ref(false);
const resolverChecks = ref<CredentialCheck[]>([]);

const credentialBadgeClasses: Record<CredentialState, string> = {
  valid: "ok",
  invalid: "err",
  unverified: "warn",
  error: "warn",
  unconfigured: ""
};

function credentialBadgeClass(state: CredentialState): string {
  return credentialBadgeClasses[state] ?? "";
}

function credentialStateLabel(check: CredentialCheck): string {
  switch (check.state) {
    case "valid":
      return check.account ? `已登录 · ${check.account}` : "已登录";
    case "invalid":
      return "未登录或已失效";
    case "unverified":
      return "无法实测";
    case "error":
      return "暂时测不了";
    default:
      return "未填写";
  }
}

async function testResolverSettings(): Promise<void> {
  testingResolver.value = true;
  try {
    const response = await testResolverCredentials(buildSettingsPayload(), clearSecrets.value);
    resolverChecks.value = response.credentials;
    const invalid = response.credentials.filter((item) => item.state === "invalid");
    const valid = response.credentials.filter((item) => item.state === "valid");
    if (invalid.length > 0) toastError(`${invalid.map((item) => item.label).join("、")} 未登录或已失效`);
    else if (valid.length > 0) toastSuccess(`${valid.length} 项凭据登录有效`);
    else toastSuccess("测试完成，没有可实测的已填凭据");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "凭据测试失败");
  } finally {
    testingResolver.value = false;
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

function upsert(state: PluginState, scope = botScope.value): void {
  if (scope !== botScope.value) return;
  state = pluginForBot(state, scope);
  const index = plugins.value.findIndex((plugin) => plugin.manifest.id === state.manifest.id);
  if (index >= 0) {
    plugins.value[index] = state;
  }
}

let reloadID = 0;
async function reload(): Promise<void> {
  const requestID = ++reloadID;
  const scope = botScope.value;
  loading.value = true;
  loadError.value = "";
  try {
    const states = await listPlugins();
    if (requestID !== reloadID || scope !== botScope.value) return;
    plugins.value = states.filter(plugin => plugin.manifest.id !== "official.open-api").map(plugin => pluginForBot(plugin, scope));
    const requestedSettings = viewQuery().get("settings");
    if (!settingsTarget.value && requestedSettings) {
      const target = plugins.value.find((plugin) => plugin.manifest.id === requestedSettings && plugin.installed);
      if (target?.manifest.settings?.length) {
        openSettings(target);
      }
    }
  } catch (error) {
    if (requestID !== reloadID || scope !== botScope.value) return;
    loadError.value = error instanceof Error ? error.message : "加载插件失败";
  } finally {
    if (requestID === reloadID) loading.value = false;
  }
}

async function toggleEnabled(plugin: PluginState): Promise<void> {
  const scope = botScope.value;
  const togglePublish = plugin.manifest.id === repositoryWatchPluginID && repositoryPublishTarget.value?.installed;
  busyID.value = plugin.manifest.id;
  const nextEnabled = !pluginEnabled(plugin);
  try {
    upsert(await setPluginEnabled(plugin.manifest.id, nextEnabled, scope), scope);
    if (togglePublish) {
      upsert(await setPluginEnabled(repositoryPublishPluginID, nextEnabled, scope), scope);
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
    message: `确定卸载「${plugin.manifest.name}」吗？卸载影响所有机器人；各机器人的设置会保留，重新安装后仍然可用。`,
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

// 从 GitHub 安装第三方插件：粘贴链接 → 检查（拉清单渲染权限与风险）→
// 勾选「我已了解风险」→ 确认安装。风险勾选框默认不勾，确认按钮随勾选状态解锁。
const repoInstallOpen = ref(false);
const repoInstallURL = ref("");
const repoPreview = ref<RepoPluginPreview | null>(null);
const repoInstallError = ref("");
const repoCheckBusy = ref(false);
const repoInstallBusy = ref(false);
const repoRiskAccepted = ref(false);
const repoReplaceAccepted = ref(false);

const repoReplaceLabel = computed(() => {
  switch (repoPreview.value?.installed?.change) {
    case "upgrade":
      return "升级";
    case "downgrade":
      return "降级";
    default:
      return "同版本覆盖";
  }
});

// 内置插件占用的 ID 装不了；同 ID 覆盖要先勾确认。
const repoInstallBlocked = computed(() => {
  const installed = repoPreview.value?.installed;
  if (!installed) {
    return false;
  }
  return installed.built_in === true || !repoReplaceAccepted.value;
});

const repoPreviewLink = computed(() => {
  const source = repoPreview.value?.source;
  if (!source) {
    return "";
  }
  return `https://github.com/${source.owner}/${source.repo}${source.ref ? `/tree/${source.ref}` : ""}`;
});

function openRepoInstall(url = ""): void {
  repoInstallOpen.value = true;
  repoInstallURL.value = url;
  resetRepoPreview();
}

// 第三方插件的更新链接：固定 ref 拼回安装时的形态，复现安装。
function repoInstallLink(plugin: PluginState): string {
  const source = plugin.repo_source;
  if (!source) {
    return "";
  }
  return `${source.url}${source.ref ? `/tree/${source.ref}` : ""}`;
}

function closeRepoInstall(): void {
  if (repoInstallBusy.value || repoCheckBusy.value) {
    return;
  }
  repoInstallOpen.value = false;
}

function resetRepoPreview(): void {
  repoPreview.value = null;
  repoInstallError.value = "";
  repoRiskAccepted.value = false;
  repoReplaceAccepted.value = false;
}

async function checkRepoPlugin(): Promise<void> {
  const url = repoInstallURL.value.trim();
  if (!url) {
    return;
  }
  repoCheckBusy.value = true;
  repoInstallError.value = "";
  try {
    repoPreview.value = await previewRepoPlugin(url);
    repoRiskAccepted.value = false;
    repoReplaceAccepted.value = false;
  } catch (error) {
    repoPreview.value = null;
    repoInstallError.value = error instanceof Error ? error.message : "检查失败";
  } finally {
    repoCheckBusy.value = false;
  }
}

async function confirmRepoInstall(): Promise<void> {
  const url = repoInstallURL.value.trim();
  const preview = repoPreview.value;
  if (!url || !preview || !repoRiskAccepted.value) {
    return;
  }
  repoInstallBusy.value = true;
  try {
    upsert(await installRepoPlugin(url, true, preview.commit, Boolean(preview.installed)));
    toastSuccess(`已安装 ${preview.manifest.name}`);
    repoInstallOpen.value = false;
    await reload();
  } catch (error) {
    repoInstallError.value = error instanceof Error ? error.message : "安装失败";
  } finally {
    repoInstallBusy.value = false;
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
  resolverChecks.value = [];
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

const pluginPlatformLabels: Record<string, string> = {
  "onebot-v11": "OneBot",
  telegram: "Telegram",
  "qq-official": "QQ 官方",
  dingtalk: "钉钉",
  feishu: "飞书",
  wecom: "企业微信"
};

function pluginPlatformBadges(plugin: PluginState): Array<{ id: string; label: string; note: string }> {
  const platforms = plugin.manifest.platforms ?? [];
  if (platforms.length === 0) return [];
  if (platforms.length === Object.keys(pluginPlatformLabels).length) {
    return [{ id: "all", label: "全平台", note: "支持当前已接入的全部聊天平台" }];
  }
  return platforms.map((id) => ({
    id,
    label: pluginPlatformLabels[id] ?? id,
    note: plugin.manifest.platform_notes?.[id] ?? ""
  }));
}

// 排列方式和 Skills、MCP 共用一份：三个标签在同一个页面里，各存各的会互相打架。
const layout = extensionLayout;
const setLayout = setExtensionLayout;

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
  if (settingsBusy.value) return;
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
  const scope = botScope.value;
  const github = isGitHubSettings.value;
  try {
    const publishPayload = github && repositoryPublishTarget.value ? buildSettingsPayload(repositoryPublishSpecs.value, repositoryPublishForm.value) : null;
    const sharedToken = String(settingsForm.value.github_token ?? "").trim();
    if (publishPayload && sharedToken) publishPayload.github_token = sharedToken;
    const publishClears = clearSecrets.value.includes("github_token") ? ["github_token"] : [];
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
    const updated = await updatePluginSettings(target.manifest.id, payload, [...clearSecrets.value]);
    upsert(updated, scope);
    if (publishPayload) {
      let publishUpdated;
      try {
        publishUpdated = await updatePluginSettings(repositoryPublishPluginID, publishPayload, publishClears);
      } catch (error) {
        // 两次请求没有事务：第一次已经落库了，这里失败会留下半保存状态。
        // 与其只弹一句「保存失败」，不如说清楚哪半边生效了，并把界面刷成真实状态。
        if (scope === botScope.value) await reload();
        const reason = error instanceof Error ? error.message : "未知错误";
        throw new Error(`Token 与仓库检查设置已保存，但 Issue 权限部分没保存成功：${reason}。请重新打开设置检查 Issue 相关配置。`);
      }
      upsert(publishUpdated, scope);
      if (scope !== botScope.value) return;
      for (const spec of repositoryPublishSpecs.value) {
        if (spec.secret) repositoryPublishForm.value[spec.key] = "";
      }
    }
    if (scope !== botScope.value) return;
    settingsTarget.value = updated;
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
  if (settingsBusy.value) return;
  // 订阅编辑器开着且有改动时，外层「保存」要连它一起提交：编辑器自己的保存会先把插件设置落库，
  // 再创建或更新订阅。校验不过或请求失败时它会自己提示，弹窗保持打开，改动不丢。
  const editor = [repositoryWatchRef.value, rssWatchRef.value].find((item) => item?.hasUnsavedChanges());
  if (editor) {
    const target = settingsTarget.value;
    savingSubscription.value = true;
    try {
      if (!(await editor.saveEditor())) return;
    } finally {
      savingSubscription.value = false;
    }
    if (target && settingsTarget.value?.manifest.id === target.manifest.id) {
      toastSuccess(`已保存 ${target.manifest.name} 的设置`);
      discardSettings();
    }
    return;
  }
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

watch(botScope, () => {
  settingsTarget.value = null;
  permissionsTarget.value = null;
  dependenciesTarget.value = null;
  plugins.value = [];
  void reload();
});

onMounted(() => {
  void reload();
  // 依赖探测会启动多个本地命令，放到浏览器空闲期，不与插件列表首屏争资源。
  const idleWindow = window as typeof window & { requestIdleCallback?: Window["requestIdleCallback"] };
  if (idleWindow.requestIdleCallback) {
    idleWindow.requestIdleCallback(() => void loadDependencies(), { timeout: 3000 });
  } else {
    window.setTimeout(() => void loadDependencies(), 1500);
  }
});
useConfigurationRefresh(["bot"], reload);

</script>
