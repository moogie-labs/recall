// Test utility only. Production entry points must not import this module.
import type {DesktopAPI,DocumentView,TreeEntry} from './types';
export function fakeBridge(documents:DocumentView[]=[],folders:string[]=[]):DesktopAPI {
 const docs=new Map(documents.map(d=>[d.id,{...d}])),dirs=new Set(folders);let version=0;
 const issue=(code:string,message:string)=>({code,message,path:''});
 return {
  openCollection:async()=>({session:'test-session',root:'/test-collection',problem:null}),
  openDocument:async(_session,id)=>({document:docs.get(id)??null,problem:docs.has(id)?null:issue('not_found','Document not found')}),
  saveBody:async input=>{const doc=docs.get(input.id);if(!doc||doc.revision!==input.expectedRevision)return {document:null,generation:input.generation,saved:false,problem:issue('conflict','Source changed'),indexingWarning:null};const saved={...doc,body:input.body,revision:`revision-${++version}`};docs.set(input.id,saved);return {document:saved,generation:input.generation,saved:true,problem:null,indexingWarning:null}},
  tree:async session=>({tree:{session,entries:[...dirs].map(path=>({id:'folder:'+path,path,name:path.split('/').at(-1)!,kind:'folder'})).concat([...docs.values()].map(({id,path,name,kind})=>({id,path,name,kind}))),diagnostics:[]},problem:null}),
  create:async input=>{
   const path=(input.parent?input.parent+'/':'')+input.name+(input.kind==='folder'?'':'.'+input.kind+'.md');
   if(dirs.has(path)||[...docs.values()].some(d=>d.path===path))return {entry:null,document:null,problem:issue('already_exists','Name already exists')};
   if(input.kind==='folder'){dirs.add(path);return {entry:{id:'folder:'+path,path,name:input.name,kind:'folder'},document:null,problem:null}}
   const doc:DocumentView={id:`test-${++version}`,path,name:input.name,title:input.name,body:'',kind:input.kind,state:input.kind==='task'?'planned':'',revision:`revision-${version}`};docs.set(doc.id,doc);return {entry:{id:doc.id,path,name:doc.name,kind:doc.kind},document:doc,problem:null};
  },
  delete:async input=>{if(input.id.startsWith('folder:')){for(const path of dirs)if(path===input.path||path.startsWith(input.path+'/'))dirs.delete(path);for(const [id,d] of docs)if(d.path.startsWith(input.path+'/'))docs.delete(id)}else{const doc=docs.get(input.id);if(doc?.revision!==input.expectedRevision)return {problem:issue('conflict','Source changed')};docs.delete(input.id)}return {problem:null}},
  search:async input=>({hits:[...docs.values()].filter(d=>(d.title+' '+d.body).toLowerCase().includes(input.query.toLowerCase())).map(d=>({...d,snippet:d.body.slice(0,150)})),problem:null}),
 };
}
