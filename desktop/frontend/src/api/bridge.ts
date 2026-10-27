import type {DesktopAPI,HostAPI,SaveBodyInput} from './types';
const backend=()=> (window as any).go.desktop.Service;
const host=()=> (window as any).go.main.Host;
export const api:DesktopAPI={openCollection:root=>backend().OpenCollection(root),openDocument:(session,id)=>backend().OpenDocument(session,id),saveBody:(input:SaveBodyInput)=>{if(!Number.isSafeInteger(input.generation)||input.generation<0)return Promise.reject(new Error('Invalid draft generation'));return backend().SaveBody(input)}};
export const hostAPI:HostAPI={ready:()=>host().Ready(),completeQuit:token=>host().CompleteQuit(token),openExternal:url=>host().OpenExternal(url),on:(name,callback)=>(window as any).runtime.EventsOn(name,callback)};
