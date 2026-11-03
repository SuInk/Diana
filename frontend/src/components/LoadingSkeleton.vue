<template>
  <div class="loading-skeleton" :class="`loading-skeleton-${kind}`" role="status" :aria-label="label" aria-busy="true">
    <!-- 机器人列表页从副标题以下整段：平台筛选、消息互通条、卡片网格。卡片按真实
         卡片的结构排（右上角启停开关、底部三个按钮），数据到了只换内容不挪位置。 -->
    <div v-if="kind === 'bots'" class="stack" aria-hidden="true">
      <div class="platform-filters">
        <SkeletonBlock v-for="width in ['76px', '69px', '105px']" :key="width" :width="width" height="33px" rounded />
      </div>
      <section class="settings-band">
        <SkeletonBlock width="34px" height="34px" />
        <div class="settings-band-copy">
          <SkeletonBlock width="64px" height="20px" />
          <SkeletonBlock width="52%" height="17px" />
        </div>
        <SkeletonBlock class="settings-band-action" width="67px" height="31px" />
      </section>
      <div class="bot-profile-grid">
        <article v-for="n in count" :key="n" class="bot-profile-tile">
          <div class="bot-profile-select">
            <div class="bot-profile-head">
              <SkeletonBlock width="38px" height="38px" />
              <SkeletonBlock width="36px" height="18px" />
            </div>
            <SkeletonBlock width="70%" height="22px" />
            <div class="bot-profile-meta">
              <SkeletonBlock width="94px" height="21px" />
              <SkeletonBlock width="64px" height="17px" />
            </div>
          </div>
          <span class="bot-profile-enable"><SkeletonBlock width="38px" height="22px" rounded /></span>
          <div class="bot-profile-actions">
            <SkeletonBlock v-for="width in ['68px', '93px', '68px']" :key="width" :width="width" height="31px" />
          </div>
        </article>
        <div class="bot-profile-add"><SkeletonBlock width="100px" height="20px" /></div>
      </div>
    </div>

    <!-- 直达某台机器人的配置（地址带 ?profile=）时，占位换成编辑页的样子：分区标签、
         当前分区的表单卡片，右侧运行状态卡。 -->
    <div v-else-if="kind === 'bot-editor'" class="grid-main-side bot-editor" aria-hidden="true">
      <div class="stack">
        <div class="editor-tabs">
          <span v-for="n in 7" :key="n" class="editor-tab" :class="{ active: n === 1 }"><SkeletonBlock width="28px" height="14px" /></span>
        </div>
        <section class="card">
          <div class="card-header"><SkeletonBlock width="132px" height="20px" /><SkeletonBlock width="112px" height="16px" /></div>
          <div class="card-body stack">
            <div v-for="n in count" :key="n" class="field"><SkeletonBlock width="96px" height="18px" /><SkeletonBlock height="37px" /></div>
          </div>
        </section>
      </div>
      <div class="stack bot-editor-sidebar">
        <section class="card">
          <div class="card-header"><SkeletonBlock width="60px" height="20px" /></div>
          <div class="card-body stack" style="gap: 10px">
            <div v-for="n in 4" :key="n" class="cluster" style="justify-content: space-between">
              <SkeletonBlock width="64px" height="18px" /><SkeletonBlock :width="n < 3 ? '52px' : '16px'" :height="n < 3 ? '22px' : '20px'" />
            </div>
          </div>
        </section>
      </div>
    </div>

    <div v-else-if="kind === 'groups'" class="group-grid" aria-hidden="true">
      <article v-for="n in count" :key="n" class="group-card">
        <div class="group-card-head">
          <div class="group-identity">
            <SkeletonBlock width="42px" height="42px" />
            <div class="skeleton-copy">
              <SkeletonBlock width="132px" height="22px" />
              <SkeletonBlock width="84px" height="17px" />
            </div>
          </div>
          <SkeletonBlock width="32px" height="18px" rounded />
        </div>
        <div class="group-card-badges">
          <SkeletonBlock v-for="badge in 3" :key="badge" width="52px" height="22px" />
        </div>
        <div class="group-card-desc skeleton-copy">
          <SkeletonBlock height="14px" /><SkeletonBlock width="62%" height="14px" />
        </div>
        <div class="group-card-foot">
          <SkeletonBlock width="70px" height="17px" />
          <div class="cluster"><SkeletonBlock width="62px" height="30px" /><SkeletonBlock width="68px" height="30px" /></div>
        </div>
      </article>
    </div>

    <div v-else-if="kind === 'plugins'" :class="layout === 'rows' ? 'plugin-rows' : 'plugin-tiles'" aria-hidden="true">
      <article v-for="n in count" :key="n" class="plugin-card">
        <div class="plugin-card-head">
          <div class="plugin-card-name"><SkeletonBlock width="126px" height="22px" /></div>
          <span class="switch"><SkeletonBlock width="32px" height="18px" rounded /></span>
        </div>
        <div class="cluster plugin-card-badges"><SkeletonBlock width="52px" height="21px" /><SkeletonBlock width="46px" height="21px" /></div>
        <div class="plugin-card-desc skeleton-copy"><SkeletonBlock height="14px" /><SkeletonBlock width="72%" height="14px" /></div>
        <div class="plugin-card-bottom">
          <div class="plugin-card-meta"><SkeletonBlock width="66px" height="17px" /></div>
          <div class="plugin-card-foot"><SkeletonBlock width="59px" height="27px" /></div>
        </div>
      </article>
    </div>

    <div v-else-if="kind === 'providers'" class="stack" aria-hidden="true" style="gap: 6px">
      <div class="group-header"><SkeletonBlock width="76px" height="20px" /><SkeletonBlock width="220px" height="18px" /></div>
      <div class="row-list">
        <div v-for="n in count" :key="n" class="row-item">
          <div class="row-main skeleton-copy"><SkeletonBlock width="160px" height="20px" /><SkeletonBlock width="65%" height="18px" /></div>
          <div class="row-actions"><SkeletonBlock width="184px" height="30px" /></div>
        </div>
      </div>
    </div>

    <div v-else-if="kind === 'users' || kind === 'notebook' || kind === 'logs'" aria-hidden="true">
      <article v-for="n in count" :key="n" class="log-row">
        <div v-if="kind === 'logs'" class="log-time"><SkeletonBlock height="17px" /></div>
        <div class="log-main">
          <div class="cluster" style="gap: 6px; margin-bottom: 2px"><SkeletonBlock width="100px" height="21px" /><SkeletonBlock width="70px" height="18px" /></div>
          <div v-if="kind === 'notebook'" class="log-detail"><SkeletonBlock width="85%" height="18px" /></div>
          <div class="log-detail"><SkeletonBlock :width="n % 2 ? '65%' : '80%'" height="18px" /></div>
          <SkeletonBlock class="skeleton-mobile-line" width="45%" height="18px" />
        </div>
        <SkeletonBlock v-if="kind !== 'logs'" width="16px" height="16px" />
      </article>
    </div>

    <div v-else-if="kind === 'tasks'" class="task-list" aria-hidden="true">
      <article v-for="n in count" :key="n" class="task-row">
        <SkeletonBlock width="34px" height="34px" />
        <div class="task-main">
          <div class="task-meta"><SkeletonBlock v-for="badge in 3" :key="badge" width="58px" height="21px" /></div>
          <div class="task-message"><SkeletonBlock width="72%" height="20px" /></div>
          <div class="task-facts"><SkeletonBlock v-for="fact in 3" :key="fact" width="140px" height="18px" /></div>
        </div>
      </article>
    </div>

    <div v-else-if="kind === 'sessions'" class="session-list" aria-hidden="true">
      <div v-for="n in count" :key="n" class="session-item">
        <div class="session-main skeleton-copy"><SkeletonBlock width="120px" height="20px" /><SkeletonBlock width="75%" height="18px" /><SkeletonBlock width="55%" height="18px" /></div>
        <SkeletonBlock width="72px" height="30px" />
      </div>
    </div>

    <div v-else-if="kind === 'form'" class="form-grid" aria-hidden="true">
      <div v-for="n in count" :key="n" class="field"><SkeletonBlock width="96px" height="20px" /><SkeletonBlock height="37px" /></div>
      <div class="field wide"><SkeletonBlock width="132px" height="38px" /></div>
    </div>

    <div v-else-if="kind === 'chart'" class="spark-plot" aria-hidden="true">
      <div class="spark-y"><SkeletonBlock v-for="n in 3" :key="n" width="22px" height="10px" /></div>
      <div class="spark-plot-main">
        <div class="spark-bars"><SkeletonBlock v-for="n in 24" :key="n" height="56px" /></div>
        <div class="spark-axis"><SkeletonBlock width="32px" height="16px" /><SkeletonBlock width="32px" height="16px" /></div>
      </div>
    </div>

    <div v-else-if="kind === 'feed'" class="event-feed" aria-hidden="true">
      <article v-for="n in count" :key="n" class="event-item">
        <div class="event-time"><SkeletonBlock width="48px" height="17px" /></div>
        <div class="event-main skeleton-copy">
          <div class="event-meta"><SkeletonBlock width="58px" height="21px" /><SkeletonBlock width="120px" height="18px" /></div>
          <SkeletonBlock width="80%" height="19px" /><SkeletonBlock width="60%" height="19px" />
        </div>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import SkeletonBlock from "./SkeletonBlock.vue";

withDefaults(defineProps<{
  kind: "bots" | "bot-editor" | "groups" | "plugins" | "providers" | "users" | "notebook" | "logs" | "tasks" | "sessions" | "form" | "chart" | "feed";
  count?: number;
  layout?: "tiles" | "rows";
  label?: string;
}>(), { count: 3, layout: "tiles", label: "正在加载" });
</script>
