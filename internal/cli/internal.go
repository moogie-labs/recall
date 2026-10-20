package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/model"
	"recall/internal/tasks"
)

func (r Runner) runWorker(ctx context.Context, args []string, logs io.Writer) int {
	// Private syntax is deliberately narrow. Never mutate descriptors, env, or
	// child ownership here: embedding.RunWorker owns the guardian FD protocol.
	if len(args) != 2 || args[0] != "--root" || !filepath.IsAbs(args[1]) {
		fmt.Fprintln(logs, "recall: private worker requires --root <absolute-root>")
		return 2
	}
	cfg, err := config.Resolve(args[1])
	if err == nil {
		worker := r.worker
		if worker == nil {
			worker = embedding.RunWorker
		}
		err = worker(ctx, cfg)
	}
	if err != nil {
		fmt.Fprintln(logs, "recall: embedding worker stopped:", model.ErrorCode(err))
		return 1
	}
	return 0
}

type referenceFlags struct{ id, path, revision string }

func (f *referenceFlags) bind(c *cobra.Command, revision bool) {
	c.Flags().StringVar(&f.id, "id", "", "Stable document ID (exclusive with --path)")
	c.Flags().StringVar(&f.path, "path", "", "Content-relative typed filename (exclusive with --id)")
	if revision {
		c.Flags().StringVar(&f.revision, "if-revision", "", "Require this source revision")
	}
}

func (f referenceFlags) ref() (model.Ref, error) {
	if (f.id == "") == (f.path == "") {
		return model.Ref{}, invalid("supply exactly one of --id or --path")
	}
	return model.Ref{ID: f.id, Path: f.path}, nil
}

type filterFlags struct {
	folder                               string
	recursive                            bool
	states, kinds                        []string
	dateField, eventState, from, through string
}

func (f *filterFlags) bindFolder(c *cobra.Command) {
	c.Flags().StringVar(&f.folder, "folder", "", "Folder filter (empty is global; . selects content root)")
	c.Flags().BoolVar(&f.recursive, "recursive", true, "Include descendant folders")
}

func (f *filterFlags) bind(c *cobra.Command, kinds bool) {
	f.bindFolder(c)
	if kinds {
		c.Flags().StringSliceVar(&f.kinds, "type", nil, "Filter note/task types (repeat or comma-separate)")
	}
	c.Flags().StringSliceVar(&f.states, "state", nil, "Current task states (repeat or comma-separate)")
	c.Flags().StringVar(&f.dateField, "date-field", "", "Task date field: created, state_changed, transition")
	c.Flags().StringVar(&f.eventState, "event-state", "", "Historical state required for transition filtering")
	c.Flags().StringVar(&f.from, "from", "", "Inclusive local date, YYYY-MM-DD")
	c.Flags().StringVar(&f.through, "through", "", "Inclusive local date, YYYY-MM-DD")
}

func (f filterFlags) parse(cfg config.Config) (model.Filter, error) {
	q := model.Filter{Folder: f.folder, Recursive: f.recursive, DateField: model.DateField(f.dateField), EventState: model.State(f.eventState)}
	for _, state := range f.states {
		if !validState(state) {
			return q, invalid("unsupported task state")
		}
		q.States = append(q.States, model.State(state))
	}
	for _, kind := range f.kinds {
		if kind != "note" && kind != "task" {
			return q, invalid("type must be note or task")
		}
		q.Kinds = append(q.Kinds, model.Kind(kind))
	}
	switch f.dateField {
	case "":
		if f.from != "" || f.through != "" || f.eventState != "" {
			return q, invalid("date bounds and event state require --date-field")
		}
	case "created", "state_changed":
		if f.eventState != "" {
			return q, invalid("--event-state requires --date-field transition")
		}
	case "transition":
		if !validState(f.eventState) {
			return q, invalid("transition filtering requires a valid --event-state")
		}
	default:
		return q, invalid("date field must be created, state_changed, or transition")
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return q, invalid("invalid collection timezone")
	}
	q.From, q.Until, err = tasks.DateRange(f.from, f.through, loc)
	return q, err
}

func validState(state string) bool {
	return state == "planned" || state == "in_progress" || state == "stalled" || state == "completed"
}

const maxInputBytes = 16 << 20

func (i *invocation) input(path string, raw bool) (string, error) {
	source := i.stdin
	if path != "" && path != "-" {
		// Nonblocking open permits rejecting FIFOs/devices instead of hanging on
		// a path advertised as a file. Input files need not be inside content.
		f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			return "", &model.Error{Code: "not_found", Path: path, Message: "input file not found"}
		}
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", invalid("input file must be a regular file")
		}
		source = f
	} else if path == "" {
		if f, ok := source.(*os.File); ok {
			info, err := f.Stat()
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeCharDevice != 0 {
				if raw {
					return "", invalid("supply raw Markdown on stdin or with --raw-file")
				}
				return "", nil // Empty creation on a terminal never launches an editor.
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(contextReader{i.ctx, source}, maxInputBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxInputBytes {
		return "", invalid("input exceeds the 16 MiB limit")
	}
	return string(body), nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	// Caller-owned arbitrary Readers cannot safely be closed or interrupted.
	// For process/file input, poll so cancellation also interrupts idle pipes.
	if f, ok := r.source.(*os.File); ok {
		for {
			fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 50)
			if err != nil && !errors.Is(err, unix.EINTR) {
				return 0, err
			}
			if err := r.ctx.Err(); err != nil {
				return 0, err
			}
			if n > 0 {
				break
			}
		}
	}
	n, err := r.source.Read(b)
	if canceled := r.ctx.Err(); canceled != nil {
		return n, canceled
	}
	return n, err
}

func pageBounds(limit, offset, maximum int) error {
	if limit < 0 || limit > maximum || offset < 0 {
		return invalid("limit or offset outside supported range")
	}
	return nil
}

func bareName(name string) error {
	if name == "" || strings.HasSuffix(name, ".note.md") || strings.HasSuffix(name, ".task.md") {
		return invalid("--name must be a nonempty bare filename component, without the type suffix")
	}
	return nil
}
