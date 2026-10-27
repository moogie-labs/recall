import type {DesktopAPI,DocumentView,Problem,TreeEntry} from '../api/types';
import {SaveCoordinator} from '../saving/coordinator';
export interface WorkspaceState {session:string;treeSelection:TreeEntry|null;document:DocumentView|null;mode:'preview'|'edit';searchActive:boolean;busy:boolean;problem:Problem|null}
export class Workspace {
 private state:WorkspaceState={session:'',treeSelection:null,document:null,mode:'preview',searchActive:false,busy:false,problem:null};
 private listeners=new Set<()=>void>();private request=0;private transitions:Promise<void>=Promise.resolve();private activeTransitions=0;
 constructor(private api:DesktopAPI,readonly coordinator:SaveCoordinator){}
 snapshot=()=>this.state;
 subscribe=(listener:()=>void)=>{this.listeners.add(listener);return ()=>{this.listeners.delete(listener)}};
 private update(patch:Partial<WorkspaceState>){this.state={...this.state,...patch};this.listeners.forEach(l=>l())}
 setCollection(session:string){this.request++;this.coordinator.clear();this.update({session,treeSelection:null,document:null,mode:'preview',searchActive:false,problem:null})}
 async selectTree(entry:TreeEntry){if(entry.kind==='folder'){this.request++;this.update({treeSelection:entry});return}await this.openDocument(entry.id)}
 async openDocument(id:string){
  const request=++this.request,session=this.state.session;
  await this.flushBefore(async()=>{const r=await this.api.openDocument(session,id);if(request!==this.request||session!==this.state.session)return;if(r.problem||!r.document)throw new Error(r.problem?.message??'Document not found');this.showDocument(r.document,'preview')});
 }
 showDocument(doc:DocumentView,mode:'preview'|'edit'){this.coordinator.open(this.state.session,doc);this.update({document:doc,treeSelection:{id:doc.id,path:doc.path,name:doc.name,kind:doc.kind},mode,searchActive:false,problem:null})}
 setMode(mode:'preview'|'edit'){this.update({mode})}
 beginSearch(){this.update({searchActive:true})}
 clearSearch(){this.update({searchActive:false})}
 async reloadCurrentAfterConfirmedDiscard(){const id=this.state.document?.id;if(!id)return;const r=await this.api.openDocument(this.state.session,id);if(r.problem||!r.document)throw new Error(r.problem?.message??'Document unavailable');this.showDocument(r.document,this.state.mode)}
 flushBefore(action:()=>Promise<void>):Promise<void>{
  this.activeTransitions++;this.update({busy:true});
  const next=this.transitions.catch(()=>{}).then(async()=>{try{await this.coordinator.flush();await action()}catch(error){this.update({problem:{code:'internal',message:(error as Error).message,path:''}});throw error}finally{this.activeTransitions--;this.update({busy:this.activeTransitions>0})}});
  this.transitions=next;return next;
 }
 clearDocument(){this.request++;this.coordinator.clear();this.update({document:null,treeSelection:null,searchActive:false,problem:null})}
 updateCleanDocument(doc:DocumentView){if(this.coordinator.snapshot().status==='saved'&&!this.coordinator.snapshot().busy){this.coordinator.open(this.state.session,doc);this.update({document:doc})}}
}
