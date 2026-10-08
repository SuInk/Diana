<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h2>钱包</h2>
        <p>她的零花钱账本。全是虚拟数字，不接任何真实支付；以后她想买东西时要你点头。</p>
      </div>
      <div class="view-actions">
        <button class="btn ghost" type="button" :disabled="loading" @click="reload">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <LoadingSkeleton v-if="loading && !space" kind="form" label="正在加载小窝" />

    <div v-else-if="space" class="stack">
      <div v-if="!space.enabled" class="card space-off">
        <div class="card-body">
          <strong>小窝还没开</strong>
          <span class="hint">到「机器人 → 人设」里打开「给她一个小窝」并保存。账本可以先在这里记着。</span>
          <button class="btn small" type="button" @click="navigate('bot')">去打开</button>
        </div>
      </div>

      <section class="card">
        <div class="card-body stack">
          <div class="space-balance">
            <div>
              <span class="muted">钱包余额</span>
              <strong>{{ formatYuan(space.wallet.balance_cents) }}</strong>
            </div>
            <button v-if="!grantOpen" class="btn" type="button" @click="grantOpen = true">
              <Plus :size="15" aria-hidden="true" />
              记一笔
            </button>
          </div>

          <form v-if="grantOpen" class="space-grant" @submit.prevent="submit">
            <div class="segmented" role="radiogroup" aria-label="记账类型">
              <button type="button" role="radio" :class="{ active: grantKind === 'allowance' }" :aria-checked="grantKind === 'allowance'" @click="grantKind = 'allowance'">发零花钱</button>
              <button type="button" role="radio" :class="{ active: grantKind === 'adjust' }" :aria-checked="grantKind === 'adjust'" @click="grantKind = 'adjust'">调账</button>
            </div>
            <div class="space-grant-fields">
              <input v-model="grantAmount" class="input" type="number" step="0.01" inputmode="decimal" :placeholder="grantKind === 'adjust' ? '金额，扣钱填负数' : '金额（元）'" aria-label="金额（元）" />
              <input v-model="grantReason" class="input" type="text" maxlength="120" placeholder="备注（可选）" aria-label="备注" />
            </div>
            <div class="space-grant-actions">
              <button class="btn ghost" type="button" @click="grantOpen = false">取消</button>
              <button class="btn" type="submit" :disabled="saving || !grantAmount">确定</button>
            </div>
          </form>

          <EmptyState v-if="space.wallet.recent.length === 0" title="还没有账目" hint="先给她发一笔零花钱吧。">
            <template #icon><Wallet :size="20" aria-hidden="true" /></template>
          </EmptyState>
          <ul v-else class="space-ledger">
            <li v-for="entry in space.wallet.recent" :key="entry.id">
              <div class="space-ledger-main">
                <span class="space-ledger-reason">{{ entry.reason || kindLabel[entry.kind] }}</span>
                <span class="muted" :title="formatTime(entry.created_at)">{{ kindLabel[entry.kind] }} · {{ formatRelative(entry.created_at) }}</span>
              </div>
              <strong :class="entry.amount_cents < 0 ? 'space-out' : 'space-in'">{{ signed(entry.amount_cents) }}</strong>
            </li>
          </ul>
        </div>
      </section>

    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { Plus, RefreshCw, Wallet } from "@lucide/vue";
import { getOwnSpace, recordOwnSpaceWallet, type OwnSpace, type WalletEntryKind } from "../api";
import { botScope } from "../bot-scope";
import { formatRelative, formatTime } from "../format";
import { navigate } from "../router";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";

const kindLabel: Record<WalletEntryKind, string> = {
  allowance: "零花钱",
  adjust: "调账",
  spend: "花费",
  refund: "退回"
};


const space = ref<OwnSpace | null>(null);
const loading = ref(true);
const saving = ref(false);
const grantOpen = ref(false);
const grantKind = ref<"allowance" | "adjust">("allowance");
const grantAmount = ref<number | string>("");
const grantReason = ref("");

function formatYuan(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

function signed(cents: number): string {
  return `${cents < 0 ? "−" : "+"}${formatYuan(Math.abs(cents))}`;
}

async function reload(): Promise<void> {
  loading.value = true;
  try {
    space.value = await getOwnSpace(botScope.value);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    loading.value = false;
  }
}

async function submit(): Promise<void> {
  // 输入框里是元，账本里是分；四舍五入到分，避免 0.1 + 0.2 这类浮点尾巴。
  const cents = Math.round(Number(grantAmount.value) * 100);
  if (!Number.isFinite(cents) || cents === 0) {
    toastError("金额不能为零");
    return;
  }
  saving.value = true;
  try {
    space.value = await recordOwnSpaceWallet(botScope.value, grantKind.value, cents, grantReason.value.trim());
    grantAmount.value = "";
    grantReason.value = "";
    grantOpen.value = false;
    toastSuccess("已记账");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  void reload();
});

watch(botScope, () => {
  void reload();
});
</script>

<style scoped>
.space-off .card-body {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}

.space-balance {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
}

.space-balance > div {
  display: grid;
  gap: 2px;
}

.space-balance strong {
  font-size: 30px;
  line-height: 1.1;
  font-variant-numeric: tabular-nums;
}

.space-grant {
  display: grid;
  gap: 10px;
  padding: 12px;
  border-radius: 12px;
  background: var(--surface-2, transparent);
}

.space-grant-fields {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr);
  gap: 8px;
}

.space-grant-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.space-ledger {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
}

.space-ledger li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--border, rgba(0, 0, 0, 0.08));
}

.space-ledger-main {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.space-ledger-main .muted {
  font-size: 12px;
}

.space-ledger-reason {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.space-ledger strong {
  flex: none;
  font-variant-numeric: tabular-nums;
}

.space-in {
  color: var(--success, inherit);
}

.space-out {
  color: var(--danger, inherit);
}

@media (max-width: 640px) {
  .space-grant-fields {
    grid-template-columns: 1fr;
  }
}
</style>
