//go:build desktopverify

package main

import (
	"context"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"os"
	"path/filepath"
)

// This method and the fixed automation script do not exist in release builds.
func (h *Host) VerifyMarker(marker string) { fmt.Fprintln(os.Stdout, marker) }
func (h *Host) VerifyExternalChange() error {
	root := os.Getenv("RECALL_HOME")
	if os.Getenv("RECALL_DESKTOP_VERIFY") != "conflict" {
		return fmt.Errorf("not a conflict fixture")
	}
	marker, err := os.ReadFile(filepath.Join(root, ".recall-native-fixture"))
	if err != nil || string(marker) != "owned verification fixture" {
		return fmt.Errorf("not an owned fixture")
	}
	file := filepath.Join(root, "content", "smoke.note.md")
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(raw, []byte("\nExternal version\n")...), 0600)
}
func verifyDOMReady(ctx context.Context) {
	mode := os.Getenv("RECALL_DESKTOP_VERIFY")
	if mode != "1" && mode != "conflict" && mode != "real" {
		return
	}
	runtime.WindowExecJS(ctx, fmt.Sprintf("window.__recallVerifyConflict=%t;", mode == "conflict"))
	runtime.WindowExecJS(ctx, fmt.Sprintf("window.__recallVerifyReal=%t;", mode == "real"))
	runtime.WindowExecJS(ctx, `(async()=>{
 const sleep=ms=>new Promise(r=>setTimeout(r,ms));
 const wait=async(fn,label)=>{for(let i=0;i<300;i++){const result=fn();if(result)return result;await sleep(40)}throw Error('Timeout: '+label)};
 const button=name=>[...document.querySelectorAll('button')].find(el=>el.textContent.trim()===name);
 const status=()=>document.querySelector('.document-footer [role=status]')?.textContent.trim();
 const insert=async text=>{const editor=await wait(()=>document.querySelector('.cm-content'),'editor');editor.focus();const sel=window.getSelection();sel.selectAllChildren(editor);sel.collapseToEnd();document.execCommand('insertText',false,text);await wait(()=>status()==='Unsaved changes','dirty state')};
 try{
  const row=await wait(()=>document.querySelector('[role=treeitem][data-path="smoke.note.md"]'),'tree');row.click();
  await wait(()=>button('Edit'),'document');button('Edit').click();await insert('\nAutosaved 自由');
  await wait(()=>status()==='Autosaving','one-second message');await wait(()=>status()==='Saved','three-second source save');
  const input=document.querySelector('input[aria-label=Search]');Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(input,'Autosaved');input.dispatchEvent(new Event('input',{bubbles:true}));
  const result=await wait(()=>{const retry=button('Search keywords instead');if(retry){if(window.__recallVerifyReal)throw Error('Real-model hybrid search failed');retry.click()}return document.querySelector('.search-hit')},'search result');result.click();
  await wait(()=>!document.querySelector('.search-results'),'result navigation');
  const create=async(kind,name,path)=>{document.querySelector('button[aria-label=Create]').click();const choice=await wait(()=>button('New '+kind),'create menu');choice.click();const field=await wait(()=>document.querySelector('dialog input'),'create dialog');Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(field,name);field.dispatchEvent(new Event('input',{bubbles:true}));await sleep(0);button('Create item').click();await wait(()=>document.querySelector('[role=treeitem][data-path="'+path+'"]'),'created '+kind);await wait(()=>!document.querySelector('dialog'),'closed create dialog')};
  await create('folder','Temporary','Temporary');await create('note','Outline','Temporary/Outline.note.md');await create('task','Checklist','Temporary/Checklist.task.md');
  document.querySelector('[role=treeitem][data-path=Temporary]').click();await wait(()=>document.querySelector('[role=treeitem][data-path=Temporary]')?.getAttribute('aria-selected')==='true','folder selection');document.querySelector('button[aria-label="Delete selection"]').click();await wait(()=>button('Delete permanently'),'recursive delete confirmation');button('Delete permanently').click();await wait(()=>!document.querySelector('[role=treeitem][data-path=Temporary]'),'deleted folder');
  document.querySelector('[role=treeitem][data-path="smoke.note.md"]').click();await wait(()=>button('Edit'),'reopen note');button('Edit').click();await insert('\nQuit flush 😀');
  if(window.__recallVerifyConflict){
   await window.go.main.Host.VerifyExternalChange();window.runtime.Quit();await wait(()=>status()==='Save failed','blocked conflicting Quit');await sleep(500);window.runtime.Quit();await sleep(500);if(status()!=='Save failed')throw Error('Conflicting quit lost draft');await window.go.main.Host.VerifyMarker('NATIVE_CONFLICT_PASS');window.confirm=()=>true;button('Reload file…').click();await wait(()=>status()==='Saved','explicit discard/reload');window.runtime.Quit();
  }else{await window.go.main.Host.VerifyMarker('NATIVE_SMOKE_PASS');window.runtime.Quit()}
 }catch(error){await window.go.main.Host.VerifyMarker('NATIVE_SMOKE_FAIL '+error.message);window.runtime.Quit()}
})()`)
}
