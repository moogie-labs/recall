// Test utility only: production entry points must never import this module.
import type {DesktopAPI,DocumentView} from './types';
export function fakeBridge(documents:DocumentView[]=[]):DesktopAPI {
 const docs=new Map(documents.map(d=>[d.id,{...d}]));let version=0;
 return {openCollection:async()=>({session:'test-session',root:'/test-collection',problem:null}),openDocument:async(_session,id)=>({document:docs.get(id)??null,problem:docs.has(id)?null:{code:'not_found',message:'Document not found',path:''}}),saveBody:async input=>{const doc=docs.get(input.id);if(!doc||doc.revision!==input.expectedRevision)return {document:null,generation:input.generation,saved:false,problem:{code:'conflict',message:'Source changed',path:''},indexingWarning:null};const saved={...doc,body:input.body,revision:`revision-${++version}`};docs.set(input.id,saved);return {document:saved,generation:input.generation,saved:true,problem:null,indexingWarning:null}}};
}
