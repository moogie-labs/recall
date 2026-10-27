import {expect,it,vi} from 'vitest';
import {Workspace} from './workspace';
import {SaveCoordinator} from '../saving/coordinator';
import {fakeBridge} from '../api/fakeBridge';
import type {DocumentReply,DocumentView} from '../api/types';
const a:DocumentView={id:'a',path:'a.note.md',name:'a',title:'A',body:'old',revision:'r0',kind:'note',state:''};
const b:DocumentView={...a,id:'b',path:'b.note.md'};
it('folder selection and preview/search preserve the draft and its save timers',async()=>{
 vi.useFakeTimers();try{const api=fakeBridge([a]);const c=new SaveCoordinator(api);const w=new Workspace(api,c);w.setCollection('test-session');await w.openDocument('a');c.edit('draft');await vi.advanceTimersByTimeAsync(500);w.setMode('preview');w.beginSearch();w.clearSearch();await w.selectTree({id:'folder:work',path:'work',name:'work',kind:'folder'});expect(w.snapshot().document?.id).toBe('a');expect(w.snapshot().treeSelection?.id).toBe('folder:work');await vi.advanceTimersByTimeAsync(500);expect(c.snapshot().status).toBe('autosaving');await vi.advanceTimersByTimeAsync(2000);expect(c.snapshot().status).toBe('saved');c.dispose()}finally{vi.useRealTimers()}
});
it('switch flushes immediately and failure blocks navigation',async()=>{
 const api=fakeBridge([a,b]);const c=new SaveCoordinator(api);const w=new Workspace(api,c);w.setCollection('test-session');await w.openDocument('a');c.edit('draft');await w.openDocument('b');expect((await api.openDocument('test-session','a')).document?.body).toBe('draft');expect(w.snapshot().document?.id).toBe('b');c.edit('conflicting draft');api.saveBody=async()=>({saved:false,document:null,generation:1,problem:{code:'conflict',message:'Changed',path:''},indexingWarning:null});await expect(w.openDocument('a')).rejects.toThrow();expect(w.snapshot().document?.id).toBe('b');expect(c.snapshot().body).toBe('conflicting draft');c.dispose();
});
it('ignores a late document load from an older collection',async()=>{
 let resolve!:(r:DocumentReply)=>void;let began!:()=>void;const started=new Promise<void>(r=>{began=r});const api=fakeBridge([a,b]);api.openDocument=()=>new Promise(r=>{resolve=r;began()});const c=new SaveCoordinator(api);const w=new Workspace(api,c);w.setCollection('old');const pending=w.openDocument('a');await started;w.setCollection('new');resolve({document:a,problem:null});await pending;expect(w.snapshot().document).toBeNull();expect(w.snapshot().session).toBe('new');c.dispose();
});
