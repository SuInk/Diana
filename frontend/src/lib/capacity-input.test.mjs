import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {computed,ref,watch,reactive,nextTick} from 'vue';
import ts from 'typescript';
const script = readFileSync(new URL('../components/CapacityInput.vue',import.meta.url),'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1].replace(/^import .*;$/gm,'');
function load(value) {
 const props=reactive({modelValue:value,id:'capacity',min:0}); const calls=[];
 const context={computed,ref,watch,defineProps:()=>props,withDefaults:p=>p,defineEmits:()=> (_event,value)=>{calls.push(value);props.modelValue=value;}};
 runInNewContext(ts.transpileModule(script+'\nglobalThis.state={unit,displayValue,updateValue};',{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.None}}).outputText,context);
 return {...context.state,props,calls};
}
test('10240 MiB 默认显示 10 GiB，切换单位不更改容量',()=>{ const s=load(10240);assert.equal(s.unit.value,'GiB');assert.equal(s.displayValue.value,10);s.unit.value='MiB';assert.equal(s.displayValue.value,10240);assert.deepEqual(s.calls,[]); });
test('GiB 小数按 MiB 保存，清空输入不会变成不限容量',async()=>{const s=load(10240);s.updateValue({target:{value:'1.5',valueAsNumber:1.5}});await nextTick();assert.equal(s.props.modelValue,1536);assert.equal(s.unit.value,'GiB');s.updateValue({target:{value:'',valueAsNumber:NaN}});assert.ok(Number.isNaN(s.props.modelValue));});
test('零表示不限，负数保留为无效值，极小正数不变成不限',()=>{const s=load(10240);for(const [input,result] of [[0,0],[-1,-1024],[0.00001,1]]) {s.updateValue({target:{value:String(input),valueAsNumber:input}});assert.equal(s.calls.at(-1),result);}});

test('小容量和不限容量也默认 GiB',()=>{for(const value of [0,512]){const s=load(value);assert.equal(s.unit.value,'GiB');assert.equal(s.displayValue.value,value/1024);}});
