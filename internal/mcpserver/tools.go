package mcpserver

import (
	"context"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/app"
	"recall/internal/model"
)

// Optional wire fields keep schemas useful to agents while queries passed to
// App retain the shared model types, defaults, validation and timestamp rules.
type refInput struct {
	ID   string `json:"id,omitempty" jsonschema:"Stable document ID; supply exactly one of id or path"`
	Path string `json:"path,omitempty" jsonschema:"Content-relative .md filename; never an absolute path"`
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
	To string `json:"to" jsonschema:"Complete content-relative destination filename including .md"`
}

type createInput struct {
	Name   string `json:"name" jsonschema:"Bare filename component; Recall appends .md"`
	Folder string `json:"folder,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

type filterInput struct {
	Folder    string `json:"folder,omitempty" jsonschema:"Empty means global; dot selects content root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"Include descendants of a specified folder; defaults false"`
}

func (in filterInput) filter() model.Filter {
	return model.Filter{Folder: in.Folder, Recursive: in.Recursive}
}

type searchInput struct {
	Filter filterInput `json:"filter,omitempty"`
	Text   string      `json:"text"`
	Mode   string      `json:"mode,omitempty" jsonschema:"hybrid (default), semantic, or keyword; keyword never starts inference"`
	Limit  int         `json:"limit,omitempty" jsonschema:"Default 20; maximum 100"`
}

type listInput struct {
	Folder    string `json:"folder,omitempty" jsonschema:"Empty means global; dot selects content root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"Include descendants of a specified folder; defaults false"`
	Desc      bool   `json:"desc,omitempty" jsonschema:"Reverse document titles only; diagnostic order and ID tie-breaks remain ascending"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Shared document-plus-diagnostic page budget; default 50, maximum 200"`
	Offset    int    `json:"offset,omitempty" jsonschema:"Nonnegative offset into diagnostics followed by documents"`
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
	Documents   []model.DocumentSummary `json:"documents"`
	Diagnostics []model.Diagnostic      `json:"diagnostics" jsonschema:"Actionable source errors; these share the page budget and cursor with documents"`
	NextOffset  *int                    `json:"next_offset"`
}
type foldersResult struct {
	Folders    []string `json:"folders"`
	NextOffset *int     `json:"next_offset"`
}
type searchResult struct {
	Hits []model.Hit `json:"hits"`
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
			if err == nil && (name == "documents_list" || name == "folders_list" || name == "search") {
				err = model.CheckPreviewData(out)
			}
			if err == nil {
				return nil, result[Out]{Data: &out}, nil
			}
			problem := model.PublicError(err)
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
	addTool(server, "documents_list", "List metadata and source diagnostics with one limit/offset budget. Diagnostics first (path/code/message ascending), then documents (title, ID); desc reverses titles only. Folder/recursive scope both. Use document_get for content.", true, false, false, func(ctx context.Context, in listInput) (documentsResult, error) {
		return listDocuments(ctx, a, in)
	})
	addTool(server, "document_get", "Get full document body, raw Markdown and revision by exactly one ID or content-relative path.", true, false, false, func(ctx context.Context, in refInput) (model.Document, error) { return a.Read(ctx, in.ref()) })
	addTool(server, "document_create", "Create a note without an editor or approval round.", false, false, false, func(ctx context.Context, in createInput) (model.Document, error) {
		return a.Create(ctx, app.CreateInput{Name: in.Name, Folder: in.Folder, Title: in.Title, Body: in.Body})
	})
	addTool(server, "document_update", "Replace complete raw Markdown; requires current revision and preserves ID. Conflicting writes fail.", false, true, false, func(ctx context.Context, in updateInput) (model.Document, error) {
		return a.Update(ctx, in.ref(), in.Revision, in.Raw)
	})
	addTool(server, "document_move", "Move a document to a full content-relative .md filename; preserves ID.", false, true, false, func(ctx context.Context, in moveInput) (model.Document, error) {
		return a.Move(ctx, in.ref(), in.To, in.Revision)
	})
	addTool(server, "document_delete", "Permanently delete a document immediately, without confirmation or trash.", false, true, false, func(ctx context.Context, in revisionInput) (mutationResult, error) {
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
		page := append([]string{}, folders[start:end]...)
		return foldersResult{Folders: page, NextOffset: next}, model.CheckFolders(page)
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
}

func listDocuments(ctx context.Context, a *app.App, in listInput) (documentsResult, error) {
	limit, err := pageLimit(in.Limit, in.Offset)
	if err != nil {
		return documentsResult{}, err
	}
	snapshot, err := a.List(ctx, in.Folder, in.Recursive)
	if err != nil {
		return documentsResult{}, err
	}
	documents := snapshot.Documents
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
	diagnostics := snapshot.Diagnostics
	slices.SortFunc(diagnostics, func(a, b model.Diagnostic) int {
		if order := strings.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		if order := strings.Compare(a.Code, b.Code); order != 0 {
			return order
		}
		return strings.Compare(a.Message, b.Message)
	})
	// One logical stream: scoped diagnostics, then filtered valid documents.
	// Neither array gets a separate allowance, including pages across the seam.
	count := len(diagnostics)
	start, end, next := pageRange(count+len(documents), limit, in.Offset)
	diagnosticPage := append([]model.Diagnostic{}, diagnostics[min(start, count):min(end, count)]...)
	documentPage := documents[max(0, start-count):max(0, end-count)]
	summaries, err := model.Summaries(documentPage)
	if err != nil {
		return documentsResult{}, err
	}
	return documentsResult{Documents: summaries, Diagnostics: diagnosticPage, NextOffset: next}, model.CheckDiagnostics(diagnosticPage)
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
