import type { DesktopAPI, HostAPI, SaveBodyInput } from "./types";
const backend = () => (window as any).go.desktop.API;
const host = () => (window as any).go.main.Host;
export const api: DesktopAPI = {
  openCollection: (root) => backend().OpenCollection(root),
  openDocument: (session, id) => backend().OpenDocument(session, id),
  saveBody: (input: SaveBodyInput) => {
    if (!Number.isSafeInteger(input.generation) || input.generation < 0)
      return Promise.reject(new Error("Invalid draft generation"));
    return backend().SaveBody(input);
  },
  tree: (session) => backend().Tree(session),
  create: (input) => backend().Create(input),
  delete: (input) => backend().Delete(input),
  search: (input) => backend().Search(input),
  refresh: (session) => backend().Refresh(session),
};
export const hostAPI: HostAPI = {
  ready: () => host().Ready(),
  completeQuit: (token) => host().CompleteQuit(token),
  cancelQuit: (token) => host().CancelQuit(token),
  chooseCollection: () => host().ChooseCollection(),
  openExternal: (url) => host().OpenExternal(url),
  copyDraft: (body) => host().CopyDraft(body),
  on: (name, callback) => (window as any).runtime.EventsOn(name, callback),
};
