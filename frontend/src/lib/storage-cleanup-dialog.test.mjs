import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import ts from 'typescript';
const source = readFileSync(new URL('../components/StorageCleanupDialog.vue', import.meta.url), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1].replace(/^import .*;$/gm, '');
async function setup(kind, { approve = true, failLoad = false, failSave = false } = {}) {
  const calls = [], events = []; let mount;
  const get = async () => { if (failLoad) throw new Error('load failed'); return { retention_days: -1, max_mb: 0 }; };
  const save = name => async policy => { calls.push([name, policy]); if (failSave) throw new Error('save failed'); return policy; };
  const context = { defineProps: () => ({kind}), defineEmits: () => name => events.push(name), ref: value => ({value}), computed: fn => ({get value() {return fn();}}), onMounted: fn => {mount = fn;}, getHistoryMediaPolicy: get, getMediaCachePolicy: get, saveHistoryMediaPolicy: save('history'), saveMediaCachePolicy: save('cache'), askConfirm: async () => approve, toastSuccess: () => {}, Error };
  const code = ts.transpileModule(source + '\n globalThis.state = {days,maxMB,valid,error,submit};', {compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None}}).outputText;
  runInNewContext(code, context); await mount();
  return { ...context.state, calls, events };
}
test('清理默认不限时不提交，读取失败也不能覆盖策略', async () => {
  const s = await setup('history'); await s.submit(); assert.equal(s.calls.length, 0);
  const failed = await setup('cache', {failLoad:true}); failed.days.value = 7; await failed.submit(); assert.equal(failed.calls.length,0);
});
test('取消确认不清理，确认后调用正确策略并刷新关闭', async () => {
  const cancelled = await setup('history', {approve:false}); cancelled.days.value = 30; await cancelled.submit(); assert.equal(cancelled.calls.length,0);
  for (const kind of ['history','cache']) { const s = await setup(kind); s.days.value = 7; await s.submit(); assert.equal(s.calls[0][0],kind); assert.equal(s.calls[0][1].retention_days,7); assert.deepEqual(s.events,['cleaned','close']); }
});
test('清理失败保留弹窗，不发出完成事件；缓存拒绝零天', async () => {
  const s = await setup('cache', {failSave:true}); s.days.value = 0; assert.equal(s.valid.value,false); s.days.value = 7; await s.submit(); assert.equal(s.error.value,'save failed'); assert.deepEqual(s.events,[]);
});
