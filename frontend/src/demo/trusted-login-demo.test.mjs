import test from 'node:test';
import assert from 'node:assert/strict';
import {createTrustedLoginDemo} from './trusted-login-demo.ts';
test('application login demo supports toggle, create, duplicate, disable and rotation without retaining credentials',async()=>{
 const api=createTrustedLoginDemo(),root='/api/auth/apps/settings';
 assert.equal((await api(root,'GET',{}).json()).enabled,false);
 await api(root,'PUT',{enabled:true});
 const app={id:'ruhua',name:'入画',profile_id:'demo-bot',disabled:false};
 const issued=await api(root+'/apps','POST',app).json();assert.ok(issued.secret);
 assert.equal(api(root+'/apps','POST',app).status,409);
 const listed=await api(root,'GET',{}).json();assert.equal(listed.enabled,true);assert.equal(JSON.stringify(listed).includes(issued.secret),false);
 await api(root+'/apps/ruhua','PATCH',{...app,disabled:true});
 assert.equal((await api(root,'GET',{}).json()).apps[0].disabled,true);
 const rotated=await api(root+'/apps/ruhua/rotate','POST',{}).json();assert.notEqual(rotated.secret,issued.secret);
 assert.equal(api('/api/unrelated','GET',{}),null);
});
