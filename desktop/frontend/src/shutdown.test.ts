import {expect,it} from 'vitest';
import {registerShutdown} from './shutdown';
import {Workspace} from './navigation/workspace';
import {SaveCoordinator} from './saving/coordinator';
import {fakeBridge} from './api/fakeBridge';
import type {HostAPI,SaveBodyReply} from './api/types';
const doc={id:'a',path:'a.note.md',name:'a',title:'A',body:'old',revision:'r0',kind:'note',state:''};
it('shutdown waits for source acknowledgement and coalesces repeated requests',async()=>{
 const api=fakeBridge([doc]);const save=api.saveBody;let release!:(r:SaveBodyReply)=>void;let began!:()=>void;const started=new Promise<void>(r=>{began=r});api.saveBody=input=>new Promise(r=>{release=reply=>r(reply);void save(input).then(result=>{pending=result;began()})});let pending!:SaveBodyReply;
 const c=new SaveCoordinator(api),w=new Workspace(api,c);w.setCollection('test-session');await w.openDocument('a');c.edit('latest');let listener!:(payload:any)=>void;let completions=0;let completed!:()=>void;const done=new Promise<void>(r=>{completed=r});const host:HostAPI={ready:async()=>{},openExternal:async()=>({problem:null}),on:(_name,fn)=>{listener=fn;return ()=>{}},completeQuit:async()=>{completions++;completed();return {problem:null}}};const stop=registerShutdown(host,w,c);listener({token:'one'});listener({token:'one'});await started;expect(completions).toBe(0);expect(w.snapshot().busy).toBe(true);release(pending);await done;expect(completions).toBe(1);stop();c.dispose();
});
it('failed shutdown preserves the draft and permits retry after repair',async()=>{
 const api=fakeBridge([doc]);const save=api.saveBody;api.saveBody=async()=>{throw new Error('Disk full')};const c=new SaveCoordinator(api),w=new Workspace(api,c);w.setCollection('test-session');await w.openDocument('a');c.edit('draft');let listener!:(payload:any)=>void;let completed=0;const host:HostAPI={ready:async()=>{},openExternal:async()=>({problem:null}),on:(_n,fn)=>{listener=fn;return ()=>{}},completeQuit:async()=>{completed++;return {problem:null}}};const stop=registerShutdown(host,w,c);listener({token:'one'});await new Promise(r=>setTimeout(r,0));expect(completed).toBe(0);expect(c.snapshot().body).toBe('draft');expect(w.snapshot().busy).toBe(false);api.saveBody=save;await c.retry();listener({token:'one'});await new Promise(r=>setTimeout(r,0));expect(completed).toBe(1);stop();c.dispose();
});
