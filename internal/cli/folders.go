package cli

import (
	"github.com/spf13/cobra"
	"recall/internal/app"
)

func (i *invocation) folderCommand() *cobra.Command {
	c := i.group("folder", "List, create, move, and permanently delete folders")
	var f filterFlags
	list := i.command("list", "List collection folders", noArgs, func(*cobra.Command, []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			snapshot, err := a.List(i.ctx, f.folder, f.recursive)
			if snapshot.Folders == nil {
				snapshot.Folders = []string{}
			}
			return snapshot.Folders, err
		})
	})
	f.bindFolder(list)
	c.AddCommand(list)
	c.AddCommand(i.command("create PATH", "Create a content-relative folder", exactArgs(1), func(_ *cobra.Command, args []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			err := a.CreateFolder(i.ctx, args[0])
			return struct {
				Path string `json:"path"`
			}{args[0]}, err
		})
	}))
	c.AddCommand(i.command("move FROM TO", "Move or rename a content-relative folder", exactArgs(2), func(_ *cobra.Command, args []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			err := a.MoveFolder(i.ctx, args[0], args[1])
			return struct {
				Path string `json:"path"`
			}{args[1]}, err
		})
	}))
	var recursive bool
	remove := i.command("delete PATH", "Permanently delete; nonempty folders require --recursive", exactArgs(1), func(_ *cobra.Command, args []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			err := a.DeleteFolder(i.ctx, args[0], recursive)
			return struct {
				Deleted bool `json:"deleted"`
			}{true}, err
		})
	})
	remove.Flags().BoolVar(&recursive, "recursive", false, "Explicitly delete this folder and all its contents")
	c.AddCommand(remove)
	return c
}
