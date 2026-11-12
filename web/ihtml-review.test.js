'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),path=require('node:path');
const {chromium}=require('playwright');
global.window={};require('./ihtml-lib.js');const {chatTrace,makeT}=window.__ihtmlLib;
let browser;test.before(async()=>{browser=await chromium.launch({headless:true})});test.after(async()=>{await browser?.close()});

test('review collections preserve every scalar target, redact values, and detect same-sized changes',()=>{
 const targets=Array.from({length:1000},(_,i)=>'target-'+i);targets[999]='api_key=PRIVATE';
 const view=chatTrace.reviewView({after:{targets}},makeT('en'));
 assert.equal(view.rows[0].afterCollection.items.length,1000);assert.equal(view.truncated,false);assert.doesNotMatch(JSON.stringify(view),/PRIVATE/);
 const changed=chatTrace.reviewView({before:{targets:targets.slice(0,10)},after:{targets:[...targets.slice(0,9),'changed']}},makeT('en'));assert.equal(changed.rows.length,1);assert.match(changed.rows[0].afterCollection.items[9],/changed/);
 const bounded=chatTrace.reviewView({after:{targets:Array(10001).fill('x')}},makeT('en'));assert.equal(bounded.truncated,true);assert.equal(bounded.rows[0].afterCollection.items.length,10000);
});

async function fixture(t,width){
 const page=await browser.newPage({viewport:{width,height:1000}});t.after(()=>page.close());const failures=[];page.on('pageerror',e=>failures.push(e.message));t.after(()=>assert.deepEqual(failures,[]));
 await page.route('**/*',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><div id="a"></div><div id="b"></div>'}));await page.goto('https://host.test');
 await page.evaluate(()=>{
  window.sockets=[];window.fetch=async url=>({ok:true,status:200,text:async()=>JSON.stringify(new URL(url).pathname.endsWith('/api/config')?{user:'alice',chat:true,params:{}}:{model_ready:true,models:[],agent_modes:['deep'],deep_approval_resume:'manual_tool_only',confirmation_scope:'session'})});
  window.WebSocket=class{static OPEN=1;constructor(){this.readyState=0;this.sent=[];sockets.push(this);queueMicrotask(()=>{this.readyState=1;this.onopen?.()})}send(v){this.sent.push(JSON.parse(v))}close(){this.readyState=3;this.onclose?.()}emit(v){this.onmessage?.({data:JSON.stringify(v)})}};
  window.EventSource=class{constructor(){throw Error('unexpected SSE')}};
 });
 await page.addStyleTag({path:path.join(__dirname,'ihtml.css')});await page.addScriptTag({path:path.join(__dirname,'ihtml-lib.js')});await page.addScriptTag({path:path.join(__dirname,'ihtml.js')});return page;
}
for(const mode of ['light','dark'])for(const width of [390,1100])test(`1000 target review stays compact and complete in live/history ${mode} ${width}`,async t=>{
 const page=await fixture(t,width);
 await page.evaluate(async({mode,width})=>{
  const targets=Array.from({length:1000},(_,i)=>'person-'+String(i+1).padStart(4,'0')+'@example.test');
  window.call={id:'review',name:'arbitrary_write',title:'批准任务 · 更新设置',risk:'medium',reason:'确认后将在下列固定范围内执行已列明的修改。',body:{id:'grant',digest:'digest'},confirmation_token:'SIGNED',confirmation_nonce:'nonce',review:{scope:'固定目标 1000 个',after:{'目标清单':targets,'操作内容':'启用所选功能'}},presentation:{confirm_text:'确认并开始任务',facts:[{label:'目标数量',value:'1000'}]}};
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();a.theme.set({mode},{persist:false});a.root.style.width=width+'px';await a.chat.send('更新所选目标');sockets[0].emit({type:'turn_start',run_id:'r',seq:1});sockets[0].emit({type:'confirmation_required',run_id:'r',seq:2,tool_call:call});sockets[0].emit({type:'assistant_done',run_id:'r',seq:3,message:'请确认'});
  window.b=ihtml.mountChat('#b',{locale:'zh-CN',initialOperations:[{id:'review',status:'pending',proposalRecorded:true,call}]});await b.ready();b.theme.set({mode},{persist:false});b.root.style.width=width+'px';
 },{mode,width});
 const collection=page.locator('#a .t-review-collection');assert.equal(await collection.evaluate(e=>e.open),false);assert.equal(await collection.locator('li').count(),0);assert.equal(await page.locator('#a .ihtml-tool').count(),1);
 const state=selector=>page.locator(selector+' .t-review-collection').evaluate(e=>({open:e.open,color:getComputedStyle(e).color,border:getComputedStyle(e).borderColor,text:e.querySelector('summary').textContent}));assert.deepEqual(await state('#a'),await state('#b'));
 const card=page.locator('#a .ihtml-tool');assert.ok((await card.boundingBox()).height<1000);assert.equal(await card.evaluate(e=>e.scrollWidth>e.clientWidth),false);await card.screenshot({path:`/tmp/ihtml-scope-card-${mode}-${width}.png`});
 await collection.locator('summary').click();await page.waitForFunction(()=>document.querySelector('#a .t-review-collection li'));assert.equal(await collection.locator('li').count(),50);
 await collection.getByRole('button',{name:'下一页'}).click();assert.match(await collection.locator('li').first().innerText(),/person-0051/);
 await collection.locator('input').fill('person-1000');assert.equal(await collection.locator('li').count(),1);assert.match(await collection.locator('li').innerText(),/person-1000/);assert.equal(await card.evaluate(e=>e.scrollWidth>e.clientWidth),false);
 await card.screenshot({path:`/tmp/ihtml-scope-card-expanded-${mode}-${width}.png`});
 // 操作状态刷新复用相同审核视图，保留用户的展开与筛选状态。
 await page.evaluate(()=>sockets[0].emit({type:'confirmation_required',run_id:'r',seq:4,tool_call:call}));assert.equal(await collection.evaluate(e=>e.open),true);assert.equal(await collection.locator('input').inputValue(),'person-1000');assert.equal(await collection.locator('li').count(),1);
 assert.equal(await page.evaluate(()=>sockets.flatMap(s=>s.sent).filter(f=>f.type==='confirm_tool'||f.type==='confirm_tools').length),0);
});
