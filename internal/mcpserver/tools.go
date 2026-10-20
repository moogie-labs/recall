package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/app"
	"recall/internal/model"
)

// Optional wire fields keep schemas useful to agents while queries passed to
// App retain the shared model types, defaults, validation and timestamp rules.
type refInput struct {
	ID   string `json:"id,omitempty" jsonschema:"Stable document ID; supply exactly one of id or path"`
	Path string `json:"path,omitempty" jsonschema:"Content-relative typed filename; never an absolute path"`
}

func (in refInput) ref() model.Ref { return model.Ref{ID: in.ID, Path: in.Path} }

type revisionInput struct {
	refInput
	Revision string `json:"if_revision,omitempty" jsonschema:"Reject if the source revision has changed"`
}

type updateInput struct {
	refInput
	Revision string `json:"if_revision" jsonschema:"Required revision from document_get"`
	Raw      string `json:"raw" jsonschema:"Complete replacement Markdown including metadata and unchanged ID"`
}

type moveInput struct {
	revisionInput
	To string `json:"to" jsonschema:"Complete content-relative destination filename including .note.md or .task.md"`
}

type createInput struct {
	Kind   model.Kind  `json:"kind" jsonschema:"note or task"`
	Name   string      `json:"name" jsonschema:"Bare filename component; Recall appends the type suffix"`
	Folder string      `json:"folder,omitempty"`
	Title  string      `json:"title,omitempty"`
	Body   string      `json:"body,omitempty"`
	State  model.State `json:"state,omitempty" jsonschema:"Tasks only; defaults to planned"`
}

type stateInput struct {
	revisionInput
	State model.State `json:"state" jsonschema:"planned, in_progress, stalled, or completed"`
}

type filterInput struct {
	Folder     string          `json:"folder,omitempty" jsonschema:"Empty means global; dot selects content root"`
	Recursive  bool            `json:"recursive,omitempty" jsonschema:"Include descendants of a specified folder; defaults false"`
	Kinds      []model.Kind    `json:"kinds,omitempty"`
	States     []model.State   `json:"states,omitempty"`
	DateField  model.DateField `json:"date_field,omitempty" jsonschema:"created, state_changed, or transition (tasks only)"`
	EventState model.State     `json:"event_state,omitempty" jsonschema:"Required for transition filtering"`
	From       *time.Time      `json:"from,omitempty" jsonschema:"Inclusive RFC3339 timestamp with timezone"`
	Until      *time.Time      `json:"until,omitempty" jsonschema:"Exclusive RFC3339 timestamp with timezone"`
}

func (in filterInput) filter() model.Filter {
	return model.Filter{Folder: in.Folder, Recursive: in.Recursive, Kinds: in.Kinds, States: in.States, DateField: in.DateField, EventState: in.EventState, From: in.From, Until: in.Until}
}

type taskQueryInput struct {
	Filter filterInput `json:"filter,omitempty"`
	Sort   string      `json:"sort,omitempty" jsonschema:"title (default), created, or state_changed; ID breaks ties"`
	Desc   bool        `json:"desc,omitempty"`
	Limit  int         `json:"limit,omitempty" jsonschema:"Default 50; maximum 200"`
	Offset int         `json:"offset,omitempty" jsonschema:"Nonnegative offset; default zero"`
}

type searchInput struct {
	Filter filterInput `json:"filter,omitempty"`
	Text   string      `json:"text"`
	Mode   string      `json:"mode,omitempty" jsonschema:"hybrid (default), semantic, or keyword; keyword never starts inference"`
	Limit  int         `json:"limit,omitempty" jsonschema:"Default 20; maximum 100"`
}

type listInput struct {
	Folder    string       `json:"folder,omitempty" jsonschema:"Empty means global; dot selects content root"`
	Recursive bool         `json:"recursive,omitempty" jsonschema:"Include descendants of a specified folder; defaults false"`
	Kinds     []model.Kind `json:"kinds,omitempty"`
	Desc      bool         `json:"desc,omitempty"`
	Limit     int          `json:"limit,omitempty" jsonschema:"Default 50; maximum 200"`
	Offset    int          `json:"offset,omitempty"`
}

type pageInput struct {
	Limit  int `json:"limit,omitempty" jsonschema:"Default 50; maximum 200"`
	Offset int `json:"offset,omitempty"`
}

type folderInput struct {
	Path string `json:"path"`
}
type folderMoveInput struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type folderDeleteInput struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"Explicitly allow permanent deletion of all descendants"`
}

type documentsResult struct {
	Documents  []model.DocumentSummary `json:"documents"`
	NextOffset *int                    `json:"next_offset"`
}
type foldersResult struct {
	Folders    []string `json:"folders"`
	NextOffset *int     `json:"next_offset"`
}
type searchResult struct {
	Hits []model.Hit `json:"hits"`
}
type historyResult struct {
	History []model.Event `json:"history"`
}
type mutationResult struct {
	OK bool `json:"ok"`
}

// Object-shaped outputs work with both initialization-based and newer clients.
// The SDK additionally emits serialized JSON text for clients without support
// for structuredContent. App errors retain stable codes without leaking causes.
type result[T any] struct {
	Data  *T           `json:"data,omitempty"`
	Error *model.Error `json:"error,omitempty"`
}

func addTool[In, Out any](server *mcp.Server, name, description string, readOnly, destructive, idempotent bool, run func(context.Context, In) (Out, error)) {
	closedWorld := false
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &closedWorld}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, result[Out], error) {
			out, err := run(ctx, in)
			if err == nil {
				return nil, result[Out]{Data: &out}, nil
			}
			problem := &model.Error{Code: model.ErrorCode(err), Message: "operation failed"}
			var typed *model.Error
			if errors.As(err, &typed) {
				problem.Message, problem.Path = typed.Message, typed.Path
			}
			if errors.Is(err, context.Canceled) {
				problem.Message = "operation canceled"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				problem.Message = "operation timed out"
			}
			return &mcp.CallToolResult{IsError: true}, result[Out]{Error: problem}, nil
		})
}

func registerTools(server *mcp.Server, a *app.App) {
	addTool(server, "search", "Search passages with shared filters; keyword is inference-free, hybrid/semantic are lazy and never silently downgraded.", true, false, false, func(ctx context.Context, in searchInput) (searchResult, error) {
		hits, err := a.Search(ctx, model.SearchQuery{Filter: in.Filter.filter(), Text: in.Text, Mode: in.Mode, Limit: in.Limit})
		if hits == nil {
			hits = []model.Hit{}
		}
		return searchResult{Hits: hits}, err
	})
	addTool(server, "documents_list", "List bounded metadata only, ordered by title then stable ID. Use document_get for body/source; empty folder is global.", true, false, false, func(ctx context.Context, in listInput) (documentsResult, error) {
		return listDocuments(ctx, a, in)
	})
	addTool(server, "document_get", "Get full document body, raw Markdown, revision and history by exactly one ID or content-relative path.", true, false, false, func(ctx context.Context, in refInput) (model.Document, error) { return a.Read(ctx, in.ref()) })
	addTool(server, "document_create", "Create a note or task without an editor or approval round.", false, false, false, func(ctx context.Context, in createInput) (model.Document, error) {
		return a.Create(ctx, app.CreateInput{Kind: in.Kind, Name: in.Name, Folder: in.Folder, Title: in.Title, Body: in.Body, State: in.State})
	})
	addTool(server, "document_update", "Replace complete raw Markdown; requires current revision and preserves ID. Conflicting writes fail.", false, true, false, func(ctx context.Context, in updateInput) (model.Document, error) {
		return a.Update(ctx, in.ref(), in.Revision, in.Raw)
	})
	addTool(server, "document_move", "Move a document to a full content-relative typed filename; preserves ID and kind.", false, true, false, func(ctx context.Context, in moveInput) (model.Document, error) {
		return a.Move(ctx, in.ref(), in.To, in.Revision)
	})
	addTool(server, "document_delete", "Permanently delete a document and its history immediately, without confirmation or trash.", false, true, false, func(ctx context.Context, in revisionInput) (mutationResult, error) {
		err := a.Delete(ctx, in.ref(), in.Revision)
		return mutationResult{OK: err == nil}, err
	})
	addTool(server, "folders_list", "List content-relative folders, including dot for the content root; bounded pagination.", true, false, false, func(ctx context.Context, in pageInput) (foldersResult, error) {
		limit, err := pageLimit(in.Limit, in.Offset)
		if err != nil {
			return foldersResult{}, err
		}
		folders, err := a.Folders(ctx)
		if err != nil {
			return foldersResult{}, err
		}
		start, end, next := pageRange(len(folders), limit, in.Offset)
		return foldersResult{Folders: append([]string{}, folders[start:end]...), NextOffset: next}, nil
	})
	addTool(server, "folder_create", "Create a content-relative folder.", false, false, false, func(ctx context.Context, in folderInput) (mutationResult, error) {
		err := a.CreateFolder(ctx, in.Path)
		return mutationResult{OK: err == nil}, err
	})
	addTool(server, "folder_move", "Move a folder and its contents within the collection, preserving document IDs.", false, true, false, func(ctx context.Context, in folderMoveInput) (mutationResult, error) {
		err := a.MoveFolder(ctx, in.From, in.To)
		return mutationResult{OK: err == nil}, err
	})
	addTool(server, "folder_delete", "Permanently delete a folder without confirmation. Nonempty folders require recursive=true; content root cannot be deleted.", false, true, false, func(ctx context.Context, in folderDeleteInput) (mutationResult, error) {
		err := a.DeleteFolder(ctx, in.Path, in.Recursive)
		return mutationResult{OK: err == nil}, err
	})
	addTool(server, "tasks_list", "Query bounded task metadata with shared filters and sorting. Dates are RFC3339 [from, until); use task_history for full events.", true, false, false, func(ctx context.Context, in taskQueryInput) (model.Page[model.DocumentSummary], error) {
		page, err := a.Tasks(ctx, model.TaskQuery{Filter: in.Filter.filter(), Sort: in.Sort, Desc: in.Desc, Limit: in.Limit, Offset: in.Offset})
		out := model.Page[model.DocumentSummary]{Items: make([]model.DocumentSummary, len(page.Items)), NextOffset: page.NextOffset}
		for n, d := range page.Items {
			out.Items[n] = model.Summarize(d)
		}
		return out, err
	})
	addTool(server, "task_set_state", "Set a task state immediately. Repeating the same state preserves exact event history.", false, true, true, func(ctx context.Context, in stateInput) (model.Document, error) {
		return a.SetState(ctx, in.ref(), in.State, in.Revision)
	})
	addTool(server, "task_history", "Get the complete source-backed task event history in source order.", true, false, false, func(ctx context.Context, in refInput) (historyResult, error) {
		d, err := a.Read(ctx, in.ref())
		if err != nil {
			return historyResult{}, err
		}
		if d.Kind != model.Task {
			return historyResult{}, &model.Error{Code: "type_mismatch", Path: d.Path, Message: "only tasks have state history"}
		}
		return historyResult{History: d.History}, nil
	})
}

func listDocuments(ctx context.Context, a *app.App, in listInput) (documentsResult, error) {
	limit, err := pageLimit(in.Limit, in.Offset)
	if err != nil {
		return documentsResult{}, err
	}
	for _, kind := range in.Kinds {
		if kind != model.Note && kind != model.Task {
			return documentsResult{}, invalid("kind must be note or task")
		}
	}
	snapshot, err := a.List(ctx, in.Folder, in.Recursive)
	if err != nil {
		return documentsResult{}, err
	}
	documents := snapshot.Documents
	if len(in.Kinds) > 0 {
		documents = slices.DeleteFunc(documents, func(d model.Document) bool { return !slices.Contains(in.Kinds, d.Kind) })
	}
	slices.SortFunc(documents, func(a, b model.Document) int {
		order := strings.Compare(a.Title, b.Title)
		if in.Desc {
			order = -order
		}
		if order == 0 {
			order = strings.Compare(a.ID, b.ID)
		}
		return order
	})
	start, end, next := pageRange(len(documents), limit, in.Offset)
	summaries := make([]model.DocumentSummary, end-start)
	for n, d := range documents[start:end] {
		summaries[n] = model.Summarize(d)
	}
	return documentsResult{Documents: summaries, NextOffset: next}, nil
}

func pageLimit(limit, offset int) (int, error) {
	if limit < 0 || limit > 200 || offset < 0 {
		return 0, invalid("limit must be 0 through 200 and offset must be nonnegative")
	}
	if limit == 0 {
		limit = 50
	}
	return limit, nil
}

func pageRange(length, limit, offset int) (int, int, *int) {
	start := min(length, offset)
	end := start + min(limit, length-start)
	var next *int
	if end < length {
		next = &end
	}
	return start, end, next
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }
