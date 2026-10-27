import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { hostAPI } from "../api/bridge";
import type { OperationReply } from "../api/types";
import { Compartment, EditorState, Transaction } from "@codemirror/state";
import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { Workspace } from "../navigation/workspace";
import { SaveCoordinator } from "../saving/coordinator";
import { MarkdownPreview } from "./MarkdownPreview";
export function DocumentPane({
  workspace,
  coordinator,
  hidden = false,
  copyDraft = (body: string) => hostAPI.copyDraft!(body),
}: {
  workspace: Workspace;
  coordinator: SaveCoordinator;
  hidden?: boolean;
  copyDraft?: (body: string) => Promise<OperationReply>;
}) {
  const state = useSyncExternalStore(workspace.subscribe, workspace.snapshot);
  const [recoveryNotice, setRecoveryNotice] = useState("");
  const save = useSyncExternalStore(
    coordinator.subscribe,
    coordinator.snapshot,
  );
  const element = useRef<HTMLDivElement>(null),
    editor = useRef<EditorView | null>(null),
    editable = useRef(new Compartment());
  useEffect(() => {
    if (!state.document || !element.current) return;
    const view = new EditorView({
      parent: element.current,
      state: EditorState.create({
        doc: coordinator.snapshot().body,
        extensions: [
          history(),
          lineNumbers(),
          markdown(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          EditorView.lineWrapping,
          editable.current.of(
            EditorView.editable.of(!workspace.snapshot().busy),
          ),
          EditorView.contentAttributes.of({ "aria-label": "Markdown editor" }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged)
              coordinator.edit(update.state.doc.toString());
          }),
        ],
      }),
    });
    editor.current = view;
    return () => {
      view.destroy();
      editor.current = null;
    };
  }, [state.document?.id, state.session, coordinator, workspace]);
  useEffect(() => {
    const view = editor.current;
    if (view && view.state.doc.toString() !== save.body && !view.composing)
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: save.body },
        annotations: Transaction.addToHistory.of(false),
      });
  }, [save.body]);
  useEffect(() => {
    editor.current?.dispatch({
      effects: editable.current.reconfigure(
        EditorView.editable.of(!state.busy),
      ),
    });
  }, [state.busy]);
  const failed = save.status === "error" || save.status === "conflict";
  return (
    <section className="document-pane" hidden={hidden} aria-label="Document">
      <div className="document-toolbar">
        <div className="breadcrumb">{state.document?.path ?? "Recall"}</div>
        {state.document && (
          <div className="modes" aria-label="Document mode">
            {(["preview", "edit"] as const).map((mode) => (
              <button
                key={mode}
                aria-pressed={state.mode === mode}
                onClick={() => workspace.setMode(mode)}
              >
                {mode === "preview" ? "Preview" : "Edit"}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="document-scroll">
        {!state.document && (
          <div className="empty">
            <h1>Your Recall collection</h1>
            <p>Select a note or task, or use + to create one.</p>
          </div>
        )}
        {state.document && (
          <>
            <div className="document-meta">
              {state.document.kind}
              {state.document.state
                ? " · " + state.document.state.replaceAll("_", " ")
                : ""}
            </div>
            <div hidden={state.mode !== "preview"}>
              <MarkdownPreview body={save.body} />
            </div>
            <div
              className="editor-container"
              ref={element}
              hidden={state.mode !== "edit"}
              onCompositionStart={() => coordinator.setComposing(true)}
              onCompositionEnd={() => coordinator.setComposing(false)}
            />
          </>
        )}
      </div>
      {failed && (
        <div className="save-error" role="alert">
          <span>{save.problem?.message}</span>
          {recoveryNotice && <p>{recoveryNotice}</p>}
          <div>
            <button
              onClick={() => {
                void copyDraft(save.body)
                  .then((r) =>
                    setRecoveryNotice(r.problem?.message ?? "Draft copied"),
                  )
                  .catch(() => setRecoveryNotice("Could not copy the draft"));
              }}
            >
              Copy draft
            </button>
            {save.status === "error" && (
              <button
                onClick={() => {
                  void coordinator.retry().catch(() => {});
                }}
              >
                Retry save
              </button>
            )}
            <button
              onClick={() => {
                if (
                  window.confirm(
                    "Discard this draft and reload the current file?",
                  )
                )
                  void workspace
                    .reloadCurrentAfterConfirmedDiscard()
                    .catch(() => {});
              }}
            >
              Reload file…
            </button>
          </div>
        </div>
      )}
      {save.indexingWarning && (
        <div className="index-warning">
          File saved; search indexing needs attention:{" "}
          {save.indexingWarning.message}
        </div>
      )}
      <footer className="document-footer">
        <span role="status" aria-live="polite">
          <i
            className={
              "saved-light " +
              (failed ? "error" : save.status === "saved" ? "saved" : "dirty")
            }
            aria-hidden="true"
          />
          {!state.document
            ? "Local collection"
            : failed
              ? "Save failed"
              : save.status === "saved"
                ? "Saved"
                : save.status === "autosaving"
                  ? "Autosaving"
                  : "Unsaved changes"}
        </span>
        <span>{state.document ? "Markdown" : ""}</span>
      </footer>
    </section>
  );
}
