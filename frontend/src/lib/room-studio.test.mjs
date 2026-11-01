import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import ts from "typescript";
import * as vue from "vue";
import * as customization from "./room-customization.ts";
function harness() {
 const props = vue.reactive({ profile: "one", pose: "phone", paused: false });
 const saved = new Map(); let rejectSave = false, resolveImage, unmount;
 const scope = vue.effectScope();
 const context = vm.createContext({ exports: {}, defineProps: () => props,
  localStorage: { getItem: k => saved.get(k) ?? null, setItem: (k,v) => { if(rejectSave) throw Error("quota"); saved.set(k,v); } },
  require: name => name === "vue" ? { ...vue, onBeforeUnmount: fn => { unmount = fn; } } : name.includes("room-customization") ? { ...customization, readRoomImage: () => new Promise(resolve => { resolveImage = resolve; }) } : {}
 });
 const source = readFileSync(new URL("../components/RoomStudio.vue", import.meta.url),"utf8").match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
 scope.run(() => vm.runInContext(ts.transpileModule(source + '\nexport const harness = { appearance, editing, preview, pose, feedback, busy, upload, save, reset };', { compilerOptions: {module:ts.ModuleKind.CommonJS} }).outputText,context));
 return { ...context.exports.harness, props,saved, failSave: () => { rejectSave = true; }, image: value => resolveImage(value), stop: () => {unmount();scope.stop();} };
}
test("saved room layout survives profile switching without sharing drafts", async () => {
 const h=harness(); try {
 h.appearance.value.size=31; h.save();
 h.props.profile="two"; await vue.nextTick(); assert.equal(h.appearance.value.size,27);
 h.appearance.value.size=20;
 h.props.profile="one"; await vue.nextTick(); assert.equal(h.appearance.value.size,31);
 h.reset(); assert.equal(h.appearance.value.size,27);
 assert.equal(JSON.parse(h.saved.values().next().value).size,31);
 } finally {h.stop();}
});
test("late uploads cannot overwrite another robot or an unmounted room", async () => {
 const h=harness();
 const upload=h.upload({target:{files:[{}],value:""}},"phone");
 h.props.profile="two"; await vue.nextTick();
 h.image("data:image/png;base64,AAAA"); await upload;
 assert.equal(h.appearance.value.images.phone,undefined); assert.equal(h.busy.value,false);
 const second=h.upload({target:{files:[{}],value:""}},"rest"); h.stop();
 h.image("data:image/png;base64,AAAA"); await second;
 assert.equal(h.appearance.value.images.rest,undefined);
});
test("storage failure preserves the saved layout and editing preview stays local", () => {
 const h=harness(); try {
 h.save(); const old=[...h.saved.values()][0];
 h.appearance.value.size=40; h.failSave(); h.save(); assert.equal([...h.saved.values()][0],old); assert.match(h.feedback.value,/保存失败/);
 h.editing.value=true; h.preview.value="desk"; assert.equal(h.pose.value,"desk"); assert.equal(h.props.pose,"phone");
 h.editing.value=false; assert.equal(h.pose.value,"phone");
 } finally {h.stop();}
});
