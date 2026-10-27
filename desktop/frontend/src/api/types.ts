export interface Problem {
  code: string;
  message: string;
  path: string;
}
export interface OperationReply {
  problem: Problem | null;
  indexingWarning?: Problem | null;
}
export interface DocumentView {
  id: string;
  path: string;
  name: string;
  title: string;
  kind: string;
  state: string;
  body: string;
  revision: string;
}
export interface CollectionReply {
  session: string;
  root: string;
  problem: Problem | null;
}
export interface DocumentReply {
  document: DocumentView | null;
  problem: Problem | null;
}
export interface SaveBodyInput {
  session: string;
  id: string;
  expectedRevision: string;
  body: string;
  generation: number;
}
export interface SaveBodyReply {
  document: DocumentView | null;
  generation: number;
  saved: boolean;
  problem: Problem | null;
  indexingWarning: Problem | null;
}
export interface DesktopAPI {
  openCollection(root: string): Promise<CollectionReply>;
  openDocument(session: string, id: string): Promise<DocumentReply>;
  saveBody(input: SaveBodyInput): Promise<SaveBodyReply>;
  tree(session: string): Promise<TreeReply>;
  create(input: CreateInput): Promise<CreateReply>;
  delete(input: DeleteInput): Promise<OperationReply>;
  search(input: SearchInput): Promise<SearchReply>;
  refresh(session: string): Promise<TreeReply>;
}
export interface SearchInput {
  session: string;
  query: string;
  mode: string;
  limit: number;
}
export interface SearchHit {
  id: string;
  path: string;
  title: string;
  snippet: string;
  kind: string;
  state: string;
}
export interface SearchReply {
  hits: SearchHit[];
  problem: Problem | null;
}
export interface TreeEntry {
  id: string;
  path: string;
  name: string;
  kind: string;
}
export interface TreeSnapshot {
  session: string;
  entries: TreeEntry[];
  diagnostics: Problem[];
}
export interface TreeReply {
  tree: TreeSnapshot | null;
  problem: Problem | null;
}
export interface CreateInput {
  session: string;
  kind: string;
  parent: string;
  name: string;
}
export interface CreateReply {
  entry: TreeEntry | null;
  document: DocumentView | null;
  problem: Problem | null;
  indexingWarning?: Problem | null;
}
export interface DeleteInput {
  session: string;
  id: string;
  path: string;
  expectedRevision: string;
  recursive: boolean;
}
export interface HostAPI {
  copyDraft?(body: string): Promise<OperationReply>;
  ready(): Promise<void>;
  completeQuit(token: string): Promise<OperationReply>;
  cancelQuit?(token: string): Promise<OperationReply>;
  chooseCollection?(): Promise<string>;
  openExternal(url: string): Promise<OperationReply>;
  on(name: string, callback: (payload: any) => void): () => void;
}
