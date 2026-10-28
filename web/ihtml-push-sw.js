'use strict';
// 可选 Web Push 只展示通知并打开同源页面，不接收或执行业务工具指令。
self.addEventListener('push', event => {
 event.waitUntil((async()=>{
  let payload={};try{payload=event.data?.json()||{};}catch{}
  let target='/';try{const parsed=new URL(String(payload.url||'/'),self.location.origin);if(parsed.origin===self.location.origin&&parsed.protocol==='https:')target=parsed.href;}catch{}
  await self.registration.showNotification(String(payload.title||'AI 助手').slice(0,120),{body:String(payload.body||'有新的处理进度，请打开查看。').slice(0,400),tag:String(payload.tag||'assistant-update').slice(0,128),renotify:false,data:{url:target}});
 })());
});
self.addEventListener('notificationclick',event=>{
 event.notification.close();event.waitUntil((async()=>{
  let target=new URL('/',self.location.origin);try{const parsed=new URL(event.notification.data?.url||'/',self.location.origin);if(parsed.origin===self.location.origin)target=parsed;}catch{}
  const windows=await self.clients.matchAll({type:'window',includeUncontrolled:true});
  for(const client of windows){if(new URL(client.url).origin===self.location.origin){await client.navigate(target.href);await client.focus();return;}}
  await self.clients.openWindow(target.href);
 })());
});
