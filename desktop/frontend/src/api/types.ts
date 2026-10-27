export interface Problem {code:string;message:string;path:string}
export interface OperationReply {problem:Problem|null}
export interface DocumentView {id:string;path:string;name:string;title:string;kind:string;state:string;body:string;revision:string}
export interface CollectionReply {session:string;root:string;problem:Problem|null}
export interface DocumentReply {document:DocumentView|null;problem:Problem|null}
export interface SaveBodyInput {session:string;id:string;expectedRevision:string;body:string;generation:number}
export interface SaveBodyReply {document:DocumentView|null;generation:number;saved:boolean;problem:Problem|null;indexingWarning:Problem|null}
export interface DesktopAPI {
 openCollection(root:string):Promise<CollectionReply>;
 openDocument(session:string,id:string):Promise<DocumentReply>;
 saveBody(input:SaveBodyInput):Promise<SaveBodyReply>;
}
export interface HostAPI {ready():Promise<void>;completeQuit(token:string):Promise<OperationReply>;openExternal(url:string):Promise<OperationReply>;on(name:string,callback:(payload:any)=>void):()=>void}
