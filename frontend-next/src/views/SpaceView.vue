<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h2>小窝</h2>
        <p>她自己的空间：零花钱、想要的东西和房间里的物件。全是虚拟账本，不接任何真实支付；她想买东西时要你点头。</p>
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

      <section class="card space-door">
        <div class="card-body">
          <span class="space-avatar" aria-hidden="true">{{ initial(botName) }}</span>
          <div>
            <h3>{{ botName }} 的小窝</h3>
            <span class="muted">{{ space.enabled ? "小窝已开" : "小窝关着" }}</span>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header">
          <div>
            <h2>钱包</h2>
            <span class="card-sub">账本只追加不修改，每一笔都留着</span>
          </div>
        </div>
        <div class="card-body stack">
          <div class="space-balance">
            <span class="muted">余额</span>
            <strong>{{ formatYuan(space.wallet.balance_cents) }}</strong>
          </div>

          <form class="space-grant" @submit.prevent="submit">
            <select v-model="grantKind" aria-label="记账类型">
              <option value="allowance">发零花钱</option>
              <option value="adjust">调账</option>
            </select>
            <input v-model="grantAmount" type="number" step="0.01" inputmode="decimal" placeholder="金额（元）" aria-label="金额（元）" />
            <input v-model="grantReason" type="text" maxlength="120" placeholder="备注，比如「十月零花钱」" aria-label="备注" />
            <button class="btn" type="submit" :disabled="saving || !grantAmount">记一笔</button>
          </form>
          <span class="hint">调账可以填负数，扣到零以下会被拒绝。她花钱只能走自己的购买提议，这里不能替她记花费。</span>

          <EmptyState v-if="space.wallet.recent.length === 0" title="还没有账目" hint="先给她发一笔零花钱吧。">
            <template #icon><Wallet :size="20" aria-hidden="true" /></template>
          </EmptyState>
          <ul v-else class="space-ledger">
            <li v-for="entry in space.wallet.recent" :key="entry.id">
              <span class="badge">{{ kindLabel[entry.kind] }}</span>
              <span class="space-ledger-reason">{{ entry.reason || "（无备注）" }}</span>
              <span class="muted" :title="formatTime(entry.created_at)">{{ formatRelative(entry.created_at) }}</span>
              <strong :class="entry.amount_cents < 0 ? 'space-out' : 'space-in'">{{ signed(entry.amount_cents) }}</strong>
            </li>
          </ul>
        </div>
      </section>

      <section v-for="block in upcoming" :key="block.title" class="card">
        <div class="card-header">
          <div>
            <h2>{{ block.title }}</h2>
            <span class="card-sub">{{ block.sub }}</span>
          </div>
          <span class="badge">即将推出</span>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { RefreshCw, Wallet } from "@lucide/vue";
import { getBotProfileConfig, getOwnSpace, recordOwnSpaceWallet, type OwnSpace, type WalletEntryKind } from "../api";
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

const upcoming = [
  { title: "待审批", sub: "她想买的东西，你点头才算买下" },
  { title: "愿望单", sub: "她自己记下的想要的东西和估价" },
  { title: "房间", sub: "她买下的、你送她的物件" }
];

const space = ref<OwnSpace | null>(null);
const loading = ref(true);
const saving = ref(false);
const botName = ref("她");
const grantKind = ref<"allowance" | "adjust">("allowance");
const grantAmount = ref<number | string>("");
const grantReason = ref("");

function initial(name: string): string {
  return Array.from(name.trim())[0]?.toUpperCase() ?? "?";
}

function formatYuan(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

function signed(cents: number): string {
  return `${cents < 0 ? "−" : "+"}${formatYuan(Math.abs(cents))}`;
}

async function loadName(): Promise<void> {
  try {
    const config = await getBotProfileConfig();
    const profiles = config.profiles?.length ? config.profiles : [config];
    const current = profiles.find((profile) => profile.id === botScope.value) ?? profiles[0];
    botName.value = current?.name || current?.id || "她";
  } catch {
    // 拿不到名字就叫「她」，不该因此让整页加载失败。
  }
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
    toastSuccess("已记账");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  void loadName();
  void reload();
});

watch(botScope, () => {
  void loadName();
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

.space-door .card-body {
  display: flex;
  align-items: center;
  gap: 14px;
}

.space-door h3 {
  margin: 0 0 2px;
}

.space-avatar {
  display: grid;
  place-items: center;
  width: 48px;
  height: 48px;
  flex: none;
  border-radius: 50%;
  background: var(--accent-soft, var(--surface-2));
  color: var(--accent, inherit);
  font-size: 20px;
  font-weight: 600;
}

.space-balance {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.space-balance strong {
  font-size: 28px;
  font-variant-numeric: tabular-nums;
}

.space-grant {
  display: grid;
  grid-template-columns: auto 140px minmax(0, 1fr) auto;
  gap: 8px;
}

.space-ledger {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 6px;
}

.space-ledger li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 10px;
}

.space-ledger-reason {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.space-ledger strong {
  font-variant-numeric: tabular-nums;
}

.space-in {
  color: var(--success, inherit);
}

.space-out {
  color: var(--danger, inherit);
}

@media (max-width: 640px) {
  .space-grant {
    grid-template-columns: 1fr 1fr;
  }

  .space-grant input[type="text"],
  .space-grant button {
    grid-column: 1 / -1;
  }

  .space-ledger li {
    grid-template-columns: auto minmax(0, 1fr) auto;
  }

  .space-ledger li > .muted {
    display: none;
  }
}
</style>
