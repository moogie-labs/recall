import {useEffect,useRef,type ReactNode} from 'react';
export function Modal({title,onCancel,children}:{title:string;onCancel:()=>void;children:ReactNode}){
 const ref=useRef<HTMLDialogElement>(null);
 useEffect(()=>{const previous=document.activeElement as HTMLElement;ref.current?.showModal();return ()=>{ref.current?.close();previous?.focus()}},[]);
 return <dialog ref={ref} aria-label={title} onCancel={event=>{event.preventDefault();onCancel()}}><h2>{title}</h2>{children}</dialog>;
}
