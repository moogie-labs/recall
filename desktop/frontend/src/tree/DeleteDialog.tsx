import {useState} from 'react';
import type {TreeEntry} from '../api/types';
import {Modal} from './Modal';
export function DeleteDialog({entry,onDelete,onCancel}:{entry:TreeEntry;onDelete:()=>Promise<void>;onCancel:()=>void}){
 const [busy,setBusy]=useState(false),[error,setError]=useState('');
 return <Modal title={'Delete “'+entry.name+'”?'} onCancel={()=>{if(!busy)onCancel()}}><p>This is permanent deletion, not macOS Trash.{entry.kind==='folder'?' The folder and everything inside it will be deleted.':''}</p>{error&&<p role="alert">{error}</p>}<div className="dialog-buttons"><button disabled={busy} onClick={onCancel}>Cancel</button><button className="danger" disabled={busy} onClick={()=>{setBusy(true);void onDelete().catch(e=>{setError(e.message);setBusy(false)})}}>Delete permanently</button></div></Modal>;
}
