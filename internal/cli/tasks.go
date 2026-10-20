package cli

import (
	"github.com/spf13/cobra"
	"recall/internal/app"
	"recall/internal/model"
)

func (i *invocation) taskCommand() *cobra.Command {
	c := i.group("task", "Query tasks, change states, and inspect history")
	var f filterFlags
	var sort string
	var desc bool
	var limit, offset int
	list := i.command("list", "Filter and sort tasks using the collection timezone", noArgs, func(*cobra.Command, []string) (any, error) {
		if err := pageBounds(limit, offset, 200); err != nil {
			return nil, err
		}
		if sort != "title" && sort != "created" && sort != "state_changed" {
			return nil, invalid("sort must be title, created, or state_changed")
		}
		cfg, err := i.config()
		if err != nil {
			return nil, err
		}
		filter, err := f.parse(cfg)
		if err != nil {
			return nil, err
		}
		return i.withApp(func(a *app.App) (any, error) {
			return a.Tasks(i.ctx, model.TaskQuery{Filter: filter, Sort: sort, Desc: desc, Limit: limit, Offset: offset})
		})
	})
	f.bind(list, false)
	list.Flags().StringVar(&sort, "sort", "title", "Sort by title, created, or state_changed")
	list.Flags().BoolVar(&desc, "desc", false, "Sort descending (ID breaks ties)")
	list.Flags().IntVar(&limit, "limit", 50, "Page size (maximum 200)")
	list.Flags().IntVar(&offset, "offset", 0, "Nonnegative page offset")
	c.AddCommand(list)
	var stateRef referenceFlags
	var state string
	change := i.command("state", "Set an explicit task state; same-state calls preserve history", noArgs, func(*cobra.Command, []string) (any, error) {
		if !validState(state) {
			return nil, invalid("--state must be planned, in_progress, stalled, or completed")
		}
		ref, err := stateRef.ref()
		if err != nil {
			return nil, err
		}
		return i.withApp(func(a *app.App) (any, error) { return a.SetState(i.ctx, ref, model.State(state), stateRef.revision) })
	})
	stateRef.bind(change, true)
	change.Flags().StringVar(&state, "state", "", "Required target state: planned, in_progress, stalled, completed")
	c.AddCommand(change)
	var historyRef referenceFlags
	history := i.command("history", "Read the source-backed state history", noArgs, func(*cobra.Command, []string) (any, error) {
		ref, err := historyRef.ref()
		if err != nil {
			return nil, err
		}
		return i.withApp(func(a *app.App) (any, error) {
			d, err := a.Read(i.ctx, ref)
			if err != nil {
				return nil, err
			}
			if d.Kind != model.Task {
				return nil, &model.Error{Code: "type_mismatch", Path: d.Path, Message: "only tasks have state history"}
			}
			return d.History, nil
		})
	})
	historyRef.bind(history, false)
	c.AddCommand(history)
	return c
}
