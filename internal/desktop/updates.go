package desktop

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"

	"recall/internal/update"
	"recall/internal/version"
)

// Updater is the part of *update.Updater the desktop uses.
type Updater interface {
	Check(context.Context) (update.Check, error)
	Fetch(context.Context, update.Check, func(stage string, done, total int64)) (string, error)
}

// UpdatePreferences is implemented by FilePreferences.
type UpdatePreferences interface {
	LoadUpdateCheck() (bool, error)
	SaveUpdateCheck(bool) error
}

type AppInfo struct {
	Version         string `json:"version"`
	Development     bool   `json:"development"`
	CheckForUpdates bool   `json:"checkForUpdates"`
}

// UpdateReply.State is "current", "available", "unsupported" (development
// build), "disabled" (launch check turned off) or "failed" (see Problem).
type UpdateReply struct {
	State      string   `json:"state"`
	Current    string   `json:"current"`
	Latest     string   `json:"latest"`
	ReleaseURL string   `json:"releaseURL"`
	Problem    *Problem `json:"problem"`
}

// UpgradeProgress is emitted as "desktop:upgrade-progress"; Stage is
// "downloading" or "verifying".
type UpgradeProgress struct {
	Stage string `json:"stage"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

type updateState struct {
	updater Updater
	current string
	handoff func(pkg string) error
	running atomic.Bool
}

// SetUpdater enables update checks. handoff receives a verified installer
// package and must open it as the app quits (see desktop/host.go).
func (s *Service) SetUpdater(u Updater, current string, handoff func(string) error) {
	s.upgrades.Store(&updateState{updater: u, current: current, handoff: handoff})
}

func (s *Service) AppInfo() AppInfo {
	info := AppInfo{Version: "dev", Development: true, CheckForUpdates: true}
	if p, ok := s.preferences.(UpdatePreferences); ok {
		info.CheckForUpdates, _ = p.LoadUpdateCheck() // unreadable: keep default
	}
	if u := s.upgrades.Load(); u != nil {
		_, valid := version.Parse(u.current)
		info.Version, info.Development = u.current, !valid
	}
	return info
}

func (s *Service) SetCheckForUpdates(enabled bool) OperationReply {
	p, ok := s.preferences.(UpdatePreferences)
	if !ok {
		return OperationReply{Problem: &Problem{Code: "runtime_unavailable", Message: "Preferences are unavailable"}}
	}
	if err := p.SaveUpdateCheck(enabled); err != nil {
		return OperationReply{Problem: &Problem{Code: "internal", Message: "Could not save the update preference"}}
	}
	return OperationReply{}
}

// CheckForUpdate asks GitHub for the latest release. A launch (non-manual)
// check makes no request when the user turned launch checks off.
func (s *Service) CheckForUpdate(manual bool) UpdateReply {
	info := s.AppInfo()
	reply := UpdateReply{Current: info.Version}
	u := s.upgrades.Load()
	switch {
	case u == nil || info.Development:
		reply.State = string(update.Unsupported)
	case !manual && !info.CheckForUpdates:
		reply.State = "disabled"
	default:
		check, err := u.updater.Check(context.Background())
		if err != nil {
			reply.State, reply.Problem = "failed", problem(err)
			break
		}
		reply.State, reply.Latest = string(check.State), check.Latest
		if check.Release != nil {
			reply.ReleaseURL = check.Release.URL
		}
	}
	return reply
}

// StartUpgrade downloads and verifies the latest release, then hands it off
// so the app quits (saving drafts) and opens it in Installer.
func (s *Service) StartUpgrade() OperationReply {
	u := s.upgrades.Load()
	if u == nil || s.AppInfo().Development {
		return OperationReply{Problem: &Problem{Code: "upgrade_unsupported", Message: "Updates are only available in release installs."}}
	}
	if !u.running.CompareAndSwap(false, true) {
		return OperationReply{Problem: &Problem{Code: "conflict", Message: "An upgrade is already in progress"}}
	}
	defer u.running.Store(false)
	ctx := context.Background()
	check, err := u.updater.Check(ctx)
	if err != nil {
		return OperationReply{Problem: problem(err)}
	}
	if check.State != update.Available {
		return OperationReply{Problem: &Problem{Code: "conflict", Message: "Recall " + check.Current + " is already up to date"}}
	}
	pkg, err := u.updater.Fetch(ctx, check, func(stage string, done, total int64) {
		s.emit("desktop:upgrade-progress", UpgradeProgress{Stage: stage, Done: done, Total: total})
	})
	if err != nil {
		return OperationReply{Problem: problem(err)}
	}
	if u.handoff == nil || u.handoff(pkg) != nil {
		os.RemoveAll(filepath.Dir(pkg))
		return OperationReply{Problem: &Problem{Code: "internal", Message: "Could not quit Recall to install the upgrade"}}
	}
	return OperationReply{}
}
