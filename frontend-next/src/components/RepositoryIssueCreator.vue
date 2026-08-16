<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>创建 GitHub Issue</h3>
        <p>仅可写入插件白名单中的仓库，提交内容会经过敏感信息检查。</p>
      </div>
      <button class="btn small" type="button" @click="toggleEditor">
        <X v-if="editing" :size="14" aria-hidden="true" />
        <CircleDot v-else :size="14" aria-hidden="true" />
        {{ editing ? "收起" : "新建 Issue" }}
      </button>
    </div>

    <form v-if="editing" class="repository-watch-editor" @submit.prevent="submit">
      <div class="form-grid repository-watch-form">
        <div class="field">
          <label for="repository-issue-repository">目标仓库</label>
          <input
            id="repository-issue-repository"
            v-model.trim="form.repository"
            class="input"
            type="text"
            placeholder="owner/repo"
            required
            @input="clearPendingConfirmation"
          />
        </div>
        <div class="field">
          <label for="repository-issue-labels">标签</label>
          <input
            id="repository-issue-labels"
            v-model="labelsDraft"
            class="input"
            type="text"
            placeholder="bug, ui（可选）"
            @input="clearPendingConfirmation"
          />
        </div>
        <div class="field wide">
          <label for="repository-issue-title">标题</label>
          <input
            id="repository-issue-title"
            v-model.trim="form.title"
            class="input"
            type="text"
            maxlength="256"
            required
            @input="clearPendingConfirmation"
          />
        </div>
        <div class="field wide">
          <label for="repository-issue-body">正文</label>
          <textarea
            id="repository-issue-body"
            v-model="form.body"
            class="textarea repository-issue-body"
            rows="9"
            maxlength="60000"
            placeholder="支持 Markdown"
            @input="clearPendingConfirmation"
          ></textarea>
        </div>
      </div>
      <div class="repository-watch-editor-actions">
        <button class="btn primary" type="submit" :disabled="submitting">
          <LoaderCircle v-if="submitting" :size="14" class="spin" aria-hidden="true" />
          <Send v-else :size="14" aria-hidden="true" />
          {{ submitting ? "正在检查" : "提交 Issue" }}
        </button>
      </div>
    </form>

    <div ref="duplicateNotice" v-if="pendingConfirmation" class="repository-issue-duplicate" role="status" aria-live="polite">
      <CircleAlert :size="18" aria-hidden="true" />
      <div>
        <strong>发现相似 Issue</strong>
        <p>{{ pendingConfirmation.message }}</p>
        <ul class="repository-issue-candidates">
          <li v-for="candidate in pendingConfirmation.candidates" :key="candidate.number">
            <a :href="candidate.url" target="_blank" rel="noreferrer">#{{ candidate.number }} · {{ candidate.title }}</a>
            <span class="badge">{{ candidate.state }}</span>
          </li>
        </ul>
      </div>
      <button class="btn small" type="button" :disabled="submitting" @click="confirmDuplicate">
        仍然新建
      </button>
    </div>

    <div v-if="created?.issue" class="repository-issue-result" role="status" aria-live="polite">
      <CircleCheck :size="17" aria-hidden="true" />
      <div>
        <strong>{{ created.repository }} #{{ created.issue.number }} · {{ created.issue.title }}</strong>
        <p>{{ created.message }}</p>
      </div>
      <a class="btn small" :href="created.issue.url" target="_blank" rel="noreferrer">
        <ExternalLink :size="14" aria-hidden="true" />
        查看 Issue
      </a>
    </div>
  </section>
</template>

<script setup lang="ts">
import { nextTick, reactive, ref } from "vue";
import { CircleAlert, CircleCheck, CircleDot, ExternalLink, LoaderCircle, Send, X } from "@lucide/vue";
import { createRepositoryIssue, type RepositoryIssueCreateResult } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";

const props = defineProps<{
  prepareAccess?: () => Promise<void>;
}>();

const editing = ref(false);
const submitting = ref(false);
const labelsDraft = ref("");
const created = ref<RepositoryIssueCreateResult | null>(null);
const pendingConfirmation = ref<RepositoryIssueCreateResult | null>(null);
const duplicateNotice = ref<HTMLElement | null>(null);
const form = reactive({ repository: "", title: "", body: "" });

function toggleEditor(): void {
  editing.value = !editing.value;
  if (!editing.value) pendingConfirmation.value = null;
}

function labels(): string[] {
  return [...new Set(labelsDraft.value.split(/[,，\n]/).map((label) => label.trim()).filter(Boolean))];
}

function clearPendingConfirmation(): void {
  pendingConfirmation.value = null;
}

async function submit(): Promise<void> {
  if (!form.repository || !form.title) return;
  const confirmed = await askConfirm({
    title: "提交 GitHub Issue",
    message: `即将向 ${form.repository} 创建 Issue「${form.title}」。提交后会立即通知仓库维护者。`,
    confirmLabel: "确认提交"
  });
  if (!confirmed) return;
  await create(false);
}

async function confirmDuplicate(): Promise<void> {
  const candidate = pendingConfirmation.value?.candidates?.[0];
  if (!candidate) return;
  const confirmed = await askConfirm({
    title: "仍然创建新 Issue",
    message: `已存在相似 Issue #${candidate.number}。确定不复用候选，仍要创建「${form.title}」吗？`,
    confirmLabel: "仍然创建"
  });
  if (!confirmed) return;
  await create(true);
}

async function create(allowDuplicate: boolean): Promise<void> {
  const confirmation = pendingConfirmation.value;
  const candidate = confirmation?.candidates?.[0];
  submitting.value = true;
  try {
    await props.prepareAccess?.();
    const result = await createRepositoryIssue({
      repository: form.repository,
      title: form.title,
      body: form.body,
      labels: labels(),
      allow_duplicate: allowDuplicate || undefined,
      confirmation_token: allowDuplicate ? confirmation?.confirmation_token : undefined,
      candidate_number: allowDuplicate ? candidate?.number : undefined
    });
    if (result.requires_confirmation && result.candidates?.length) {
      pendingConfirmation.value = result;
      await nextTick();
      duplicateNotice.value?.scrollIntoView({ behavior: "smooth", block: "nearest" });
      return;
    }
    if (!result.ok || !result.issue) {
      throw new Error(result.message || "Issue 提交失败");
    }
    created.value = result;
    pendingConfirmation.value = null;
    form.title = "";
    form.body = "";
    labelsDraft.value = "";
    editing.value = false;
    toastSuccess(result.redactions ? `Issue #${result.issue.number} 已创建，敏感内容已脱敏` : `Issue #${result.issue.number} 已创建`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "Issue 提交失败");
  } finally {
    submitting.value = false;
  }
}
</script>
