package cli

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"recall/internal/app"
	"recall/internal/editor"
	"recall/internal/model"
)

type documentList struct {
	Documents   []model.DocumentSummary `json:"documents"`
	Diagnostics []model.Diagnostic      `json:"diagnostics"`
	Folders     []string                `json:"folders"`
	NextOffset  *int                    `json:"next_offset"`
}

func (i *invocation) documentCommand() *cobra.Command {
	c := i.group("document", "List, read, create, and mutate notes and tasks")
	c.AddCommand(i.documentListCommand(), i.documentCreateCommand())
	for _, verb := range []string{"get", "update", "edit", "move", "delete"} {
		c.AddCommand(i.documentMutationCommand(verb))
	}
	return c
}

func (i *invocation) documentListCommand() *cobra.Command {
	var f filterFlags
	var kind string
	var limit, offset int
	var desc bool
	c := i.command("list", "List documents by title (paginated; includes diagnostics and folders)", noArgs, func(*cobra.Command, []string) (any, error) {
		if err := pageBounds(limit, offset, 200); err != nil {
			return nil, err
		}
		if kind != "" && kind != "note" && kind != "task" {
			return nil, invalid("type must be note or task")
		}
		return i.withApp(func(a *app.App) (any, error) {
			snapshot, err := a.List(i.ctx, f.folder, f.recursive)
			if err != nil {
				return nil, err
			}
			if kind != "" {
				snapshot.Documents = slices.DeleteFunc(snapshot.Documents, func(d model.Document) bool { return d.Kind != model.Kind(kind) })
			}
			slices.SortFunc(snapshot.Documents, func(a, b model.Document) int {
				order := strings.Compare(a.Title, b.Title)
				if desc {
					order = -order
				}
				if order == 0 {
					order = strings.Compare(a.ID, b.ID)
				}
				return order
			})
			if limit == 0 {
				limit = 50
			}
			start := min(offset, len(snapshot.Documents))
			end := start + min(limit, len(snapshot.Documents)-start)
			var next *int
			if end < len(snapshot.Documents) {
				next = &end
			}
			snapshot.Documents = snapshot.Documents[start:end]
			summaries := make([]model.DocumentSummary, len(snapshot.Documents))
			for n, d := range snapshot.Documents {
				summaries[n] = model.Summarize(d)
			}
			return documentList{Documents: summaries, Diagnostics: snapshot.Diagnostics, Folders: snapshot.Folders, NextOffset: next}, nil
		})
	})
	f.bindFolder(c)
	c.Flags().StringVar(&kind, "type", "", "Filter note or task documents")
	c.Flags().IntVar(&limit, "limit", 50, "Page size (maximum 200)")
	c.Flags().IntVar(&offset, "offset", 0, "Nonnegative page offset")
	c.Flags().BoolVar(&desc, "desc", false, "Sort titles descending (ID breaks ties)")
	return c
}

func (i *invocation) documentCreateCommand() *cobra.Command {
	var kind, folder, name, title, bodyFile string
	c := i.command("create", "Create from stdin or --body-file; never launches an editor", noArgs, func(*cobra.Command, []string) (any, error) {
		if kind != "note" && kind != "task" {
			return nil, invalid("--type note or task is required")
		}
		if err := bareName(name); err != nil {
			return nil, err
		}
		body, err := i.input(bodyFile, false)
		if err != nil {
			return nil, err
		}
		return i.withApp(func(a *app.App) (any, error) {
			return a.Create(i.ctx, app.CreateInput{Kind: model.Kind(kind), Folder: folder, Name: name, Title: title, Body: body})
		})
	})
	c.Flags().StringVar(&kind, "type", "", "Required document type: note or task")
	c.Flags().StringVar(&folder, "folder", "", "Destination folder (default: content root)")
	c.Flags().StringVar(&name, "name", "", "Required bare filename component; Recall appends .note.md/.task.md")
	c.Flags().StringVar(&title, "title", "", "Display title (default: name)")
	c.Flags().StringVar(&bodyFile, "body-file", "", "Read body from file (- or omitted: stdin; terminal: empty body)")
	return c
}

func (i *invocation) documentMutationCommand(verb string) *cobra.Command {
	var f referenceFlags
	var rawFile, destination string
	c := i.command(verb, map[string]string{"get": "Read complete source Markdown", "update": "Replace raw Markdown with a required source revision", "edit": "Explicitly launch the configured editor on the real source", "move": "Move to a full content-relative typed filename", "delete": "Permanently delete without prompting"}[verb], noArgs, func(*cobra.Command, []string) (any, error) {
		ref, err := f.ref()
		if err != nil {
			return nil, err
		}
		var raw string
		if verb == "update" {
			if f.revision == "" {
				return nil, invalid("raw update requires --if-revision")
			}
			raw, err = i.input(rawFile, true)
			if err != nil {
				return nil, err
			}
		}
		if verb == "move" && destination == "" {
			return nil, invalid("move requires --to with a complete typed filename")
		}
		return i.withApp(func(a *app.App) (any, error) {
			switch verb {
			case "get":
				return a.Read(i.ctx, ref)
			case "update":
				return a.Update(i.ctx, ref, f.revision, raw)
			case "move":
				return a.Move(i.ctx, ref, destination, f.revision)
			case "delete":
				if err := a.Delete(i.ctx, ref, f.revision); err != nil {
					return nil, err
				}
				return struct {
					Deleted bool `json:"deleted"`
				}{true}, nil
			case "edit":
				return i.edit(a, ref)
			}
			panic("unreachable document command")
		})
	})
	f.bind(c, verb == "update" || verb == "move" || verb == "delete")
	if verb == "update" {
		c.Flags().StringVar(&rawFile, "raw-file", "", "Complete Markdown file (- or omitted: stdin)")
	}
	if verb == "move" {
		c.Flags().StringVar(&destination, "to", "", "Required full relative destination, including the type suffix")
	}
	return c
}

func (i *invocation) edit(a *app.App, ref model.Ref) (any, error) {
	d, err := a.Read(i.ctx, ref)
	if err != nil {
		return nil, err
	}
	cfg, err := i.config()
	if err != nil {
		return nil, err
	}
	command, err := editor.Command(cfg.Editor, filepath.Join(cfg.Content, d.Path))
	if err != nil {
		return nil, err
	}
	command = exec.CommandContext(i.ctx, command.Args[0], command.Args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = i.stdin, i.stdout, i.stderr
	if i.json {
		command.Stdout = i.stderr
	}
	command.WaitDelay = 2 * time.Second
	exitErr := command.Run()
	if exitErr != nil {
		exitErr = errors.Join(&model.Error{Code: "internal", Message: "editor failed; saved changes will still be reconciled"}, i.ctx.Err())
	}
	err = editor.Complete(i.ctx, exitErr, func(ctx context.Context) error { _, err := a.Sync(ctx); return err })
	if err != nil {
		return nil, err
	}
	return a.Read(i.ctx, model.Ref{Path: d.Path})
}
