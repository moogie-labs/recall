package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"recall/internal/model"
	"recall/internal/update"
	"recall/internal/version"
)

// Upgrader is the update workflow; *update.Updater implements it.
type Upgrader interface {
	Check(context.Context) (update.Check, error)
	Fetch(context.Context, update.Check, func(stage string, done, total int64)) (string, error)
	Open(context.Context, string) error
}

type versionInfo struct {
	Version string `json:"version"`
}

func (i *invocation) versionResult() any {
	if i.json {
		return versionInfo{version.Version}
	}
	return "recall " + version.Version + "\n"
}

func (i *invocation) versionCommand() *cobra.Command {
	return i.command("version", "Print the Recall version", noArgs, func(*cobra.Command, []string) (any, error) {
		return i.versionResult(), nil
	})
}

type upgradeReport struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	Package         string `json:"package,omitempty"`
	OpenedInstaller bool   `json:"opened_installer"`
}

func (i *invocation) upgradeCommand() *cobra.Command {
	var checkOnly bool
	c := i.command("upgrade", "Download, verify, and open the newest Recall installer", noArgs, func(*cobra.Command, []string) (any, error) {
		u := i.runner.updater()
		check, err := u.Check(i.ctx)
		if err != nil {
			return nil, err
		}
		if check.State == update.Unsupported {
			return nil, &model.Error{Code: "upgrade_unsupported", Message: "this is a development build of Recall; install releases from " + update.ReleasesPage}
		}
		report := upgradeReport{Current: check.Current, Latest: check.Latest, UpdateAvailable: check.State == update.Available}
		if check.Release != nil {
			report.ReleaseURL = check.Release.URL
		}
		if !report.UpdateAvailable {
			return i.upgradeResult(report, "Recall "+check.Current+" is up to date.\n"), nil
		}
		if checkOnly {
			return i.upgradeResult(report, fmt.Sprintf("Recall %s is available (installed: %s).\nRelease notes: %s\nRun recall upgrade to install it.\n", check.Latest, check.Current, report.ReleaseURL)), nil
		}
		last := -10
		pkg, err := u.Fetch(i.ctx, check, func(stage string, done, total int64) {
			if stage == "verifying" {
				fmt.Fprintln(i.stderr, "Verifying installer signature…")
				return
			}
			if total <= 0 {
				return
			}
			percent := int(done * 100 / total)
			if percent >= last+10 || percent == 100 && last != 100 {
				fmt.Fprintf(i.stderr, "Downloading Recall %s: %d%%\n", check.Latest, percent)
				last = percent
			}
		})
		if err != nil {
			return nil, err
		}
		if err := u.Open(i.ctx, pkg); err != nil {
			return nil, err
		}
		report.Package, report.OpenedInstaller = pkg, true
		return i.upgradeResult(report, "Opened Installer for Recall "+check.Latest+". Quit the Recall app before continuing.\n"), nil
	})
	c.Flags().BoolVar(&checkOnly, "check", false, "Only report whether a newer release is available")
	return c
}

func (i *invocation) upgradeResult(report upgradeReport, text string) any {
	if i.json {
		return report
	}
	return text
}
