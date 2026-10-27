import {afterEach,beforeEach,expect,it,vi} from 'vitest';
import {SaveCoordinator} from './coordinator';
import type {DocumentView,SaveBodyInput,SaveBodyReply} from '../api/types';
export const doc:DocumentView={id:'a',path:'one.note.md',name:'one',title:'One',kind:'note',state:'',body:'old',revision:'r0'};
export function deferred<T>(){let resolve!:(value:T)=>void;let reject!:(error:unknown)=>void;const promise=new Promise<T>((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}}
export const saved=(input:SaveBodyInput):SaveBodyReply=>({document:{...doc,body:input.body,revision:`r${input.generation}`},generation:input.generation,saved:true,problem:null,indexingWarning:null});
beforeEach(()=>vi.useFakeTimers());afterEach(()=>vi.useRealTimers());
it('shows requested timing and waits for disk acknowledgement',async()=>{
 const response=deferred<SaveBodyReply>();const writes:SaveBodyInput[]=[];const c=new SaveCoordinator({saveBody:input=>{writes.push(input);return response.promise}});c.open('s',doc);c.edit('new');
 await vi.advanceTimersByTimeAsync(999);expect(c.snapshot().status).toBe('dirty');expect(writes).toHaveLength(0);
 await vi.advanceTimersByTimeAsync(1);expect(c.snapshot().status).toBe('autosaving');expect(writes).toHaveLength(0);
 await vi.advanceTimersByTimeAsync(1999);expect(writes).toHaveLength(0);await vi.advanceTimersByTimeAsync(1);expect(writes).toHaveLength(1);expect(c.snapshot().status).toBe('autosaving');response.resolve(saved(writes[0]));await c.flush();expect(c.snapshot().status).toBe('saved');c.dispose();
});
it('resumed typing resets both timers',async()=>{
 const writes:SaveBodyInput[]=[];const c=new SaveCoordinator({saveBody:async i=>{writes.push(i);return saved(i)}});c.open('s',doc);c.edit('first');await vi.advanceTimersByTimeAsync(2000);expect(c.snapshot().status).toBe('autosaving');c.edit('second');expect(c.snapshot().status).toBe('dirty');await vi.advanceTimersByTimeAsync(999);expect(c.snapshot().status).toBe('dirty');await vi.advanceTimersByTimeAsync(1);expect(c.snapshot().status).toBe('autosaving');await vi.advanceTimersByTimeAsync(1999);expect(writes).toHaveLength(0);await vi.advanceTimersByTimeAsync(1);expect(writes.map(w=>w.body)).toEqual(['second']);expect(c.snapshot().status).toBe('saved');c.dispose();
});
it('flush serializes and drains the newest generation using the acknowledged revision',async()=>{
 const response=deferred<SaveBodyReply>();const writes:SaveBodyInput[]=[];const c=new SaveCoordinator({saveBody:async i=>{writes.push(i);return writes.length===1?response.promise:saved(i)}});c.open('s',doc);c.edit('first');await vi.advanceTimersByTimeAsync(3000);c.edit('second');const flushed=c.flush();expect(writes).toHaveLength(1);response.resolve(saved(writes[0]));await flushed;expect(writes.map(w=>[w.body,w.expectedRevision])).toEqual([['first','r0'],['second','r1']]);expect(c.snapshot().status).toBe('saved');c.dispose();
});
it('failed and conflicting saves preserve drafts and never retry automatically',async()=>{
 let attempts=0;const c=new SaveCoordinator({saveBody:async i=>{attempts++;return {...saved(i),saved:false,document:null,problem:{code:'conflict',message:'Changed outside',path:doc.path}}}});c.open('s',doc);c.edit('draft');await vi.advanceTimersByTimeAsync(3000);expect(c.snapshot().status).toBe('conflict');expect(c.snapshot().body).toBe('draft');await vi.advanceTimersByTimeAsync(10000);expect(attempts).toBe(1);await expect(c.flush()).rejects.toThrow();await expect(c.retry()).rejects.toThrow();expect(attempts).toBe(1);c.dispose();
});
it('supports explicit retry of a transient write failure without losing the body',async()=>{
 let attempts=0;const c=new SaveCoordinator({saveBody:async i=>{if(++attempts===1)throw new Error('Disk unavailable');return saved(i)}});c.open('s',doc);c.edit('draft');await vi.advanceTimersByTimeAsync(3000);expect(c.snapshot().status).toBe('error');await c.retry();expect(c.snapshot().body).toBe('draft');expect(c.snapshot().status).toBe('saved');c.dispose();
});
it('does not let an old document completion mark a different document saved',async()=>{
 const response=deferred<SaveBodyReply>();let input!:SaveBodyInput;const c=new SaveCoordinator({saveBody:i=>{input=i;return response.promise}});c.open('old-session',doc);c.edit('first');await vi.advanceTimersByTimeAsync(3000);c.open('new-session',{...doc,id:'b',body:'B'});response.resolve(saved(input));await Promise.resolve();await Promise.resolve();expect(c.snapshot().body).toBe('B');expect(c.snapshot().status).toBe('saved');c.dispose();
});
it('composition postpones automatic saving until composition end',async()=>{
 const writes:SaveBodyInput[]=[];const c=new SaveCoordinator({saveBody:async i=>{writes.push(i);return saved(i)}});c.open('s',doc);c.setComposing(true);c.edit('自');await vi.advanceTimersByTimeAsync(5000);expect(writes).toHaveLength(0);c.edit('自由');c.setComposing(false);await vi.advanceTimersByTimeAsync(2999);expect(writes).toHaveLength(0);await vi.advanceTimersByTimeAsync(1);expect(writes[0].body).toBe('自由');c.dispose();
});
it('queues a due autosave when an older disk write is still in flight',async()=>{
 const response=deferred<SaveBodyReply>();const writes:SaveBodyInput[]=[];const c=new SaveCoordinator({saveBody:async input=>{writes.push(input);return writes.length===1?response.promise:saved(input)}});c.open('s',doc);c.edit('first');await vi.advanceTimersByTimeAsync(3000);c.edit('second');await vi.advanceTimersByTimeAsync(3000);expect(writes).toHaveLength(1);response.resolve(saved(writes[0]));await vi.advanceTimersByTimeAsync(0);expect(writes.map(w=>w.body)).toEqual(['first','second']);expect(c.snapshot().status).toBe('saved');c.dispose();
});
