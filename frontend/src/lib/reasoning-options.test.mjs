import test from 'node:test';
import assert from 'node:assert/strict';
import { reasoningOptions } from './reasoning-options.ts';

test('unsupported saved values remain visible but disabled; default stays available', () => {
 const options=reasoningOptions('none',['low','medium','high','xhigh','max'],'default','跟随模型');
 for (const level of ['none','minimal']) assert.equal(options.find(o=>o.value===level).disabled,true);
 for (const level of ['default','low','medium','high','xhigh','max']) assert.equal(options.find(o=>o.value===level).disabled,false);
 assert.match(options.find(o=>o.value==='none').hint,/自动适配/);
});
test('unknown models do not invent restrictions and legacy values are visible',()=>{
 const options=reasoningOptions('ultra',undefined,'','跟随提供商配置');
 assert.ok(options.some(o=>o.value==='ultra'));
 assert.ok(options.every(o=>!o.disabled));
});
test('models supporting none can still disable thinking',()=>{
 assert.equal(reasoningOptions('none',['none','low'],'','默认').find(o=>o.value==='none').disabled,false);
});
