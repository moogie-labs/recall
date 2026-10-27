import {useState} from 'react';
import {Modal} from './Modal';
export function CreateDialog({kind,parent,onCreate,onCancel}:{kind:string;parent:string;onCreate:(name:string)=>Promise<void>;onCancel:()=>void}){
 const [name,setName]=useState(''),[busy,setBusy]=useState(false),[error,setError]=useState('');
 return <Modal title={'New '+kind} onCancel={()=>{if(!busy)onCancel()}}><form onSubmit={event=>{event.preventDefault();if(!name.trim()||name.includes('/')||name==='.'||name==='..'){setError('Enter a single filename');return}setBusy(true);void onCreate(name.trim()).catch(error=>{setError(error.message);setBusy(false)})}}><p>Create in {parent||'collection root'}.</p><label>Name<input autoFocus aria-label="Name" value={name} onChange={e=>setName(e.target.value)} disabled={busy}/></label>{error&&<p role="alert">{error}</p>}<div className="dialog-buttons"><button type="button" disabled={busy} onClick={onCancel}>Cancel</button><button className="primary" disabled={busy} type="submit">Create item</button></div></form></Modal>;
}
