import React from 'react';
import {createRoot} from 'react-dom/client';
declare global {interface Window {go:{main:{Host:{Ready():Promise<void>;CompleteQuit(token:string):Promise<unknown>}}};runtime:{EventsOn(name:string,callback:(payload:{token:string})=>void):()=>void;Quit():void}}}
window.runtime.EventsOn('desktop:quit-requested',({token})=>{setTimeout(()=>void window.go.main.Host.CompleteQuit(token),1500)});
void window.go.main.Host.Ready();
setTimeout(()=>window.runtime.Quit(),2000);
createRoot(document.getElementById('root')!).render(<React.StrictMode><h1>Recall desktop compatibility fixture</h1><p>Closing waits 1.5 seconds for the frontend save acknowledgement.</p></React.StrictMode>);
