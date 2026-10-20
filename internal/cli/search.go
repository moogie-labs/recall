package cli

import (
	"github.com/spf13/cobra"
	"recall/internal/app"
	"recall/internal/model"
)

func (i *invocation) searchCommand() *cobra.Command {
	var f filterFlags
	var mode string
	var limit int
	c := i.command("search TEXT", "Search source passages (default: hybrid; keyword never loads inference)", exactArgs(1), func(_ *cobra.Command, args []string) (any, error) {
		if mode != "keyword" && mode != "semantic" && mode != "hybrid" {
			return nil, invalid("mode must be keyword, semantic, or hybrid")
		}
		if err := pageBounds(limit, 0, 100); err != nil {
			return nil, err
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
			return a.Search(i.ctx, model.SearchQuery{Filter: filter, Text: args[0], Mode: mode, Limit: limit})
		})
	})
	f.bind(c, true)
	c.Flags().StringVar(&mode, "mode", "hybrid", "keyword, semantic, or hybrid")
	c.Flags().IntVar(&limit, "limit", 20, "Maximum hits (maximum 100)")
	return c
}
